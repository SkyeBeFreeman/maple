// Package tools defines the fake MCP server shared by the fakemcp binary and
// the in-process HTTP fakes, so both expose an identical tool set.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configures the fake server.
type Options struct {
	Name, Instructions, Description string
	Extra                           int
	BadSchema                       bool
}

type echoIn struct {
	Text string `json:"text"`
}

type sleepIn struct {
	MS int `json:"ms"`
}

type crashIn struct {
	Code int `json:"code"`
}

type empty struct{}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// NewServer builds a fake MCP server with echo, env, sleep, crash, t0..tN-1
// and optionally a tool "bad" whose input schema is not an object.
func NewServer(o Options) *mcp.Server {
	name := o.Name
	if name == "" {
		name = "fakemcp"
	}
	s := mcp.NewServer(&mcp.Implementation{Name: name, Description: o.Description, Version: "0.0.1"},
		&mcp.ServerOptions{Instructions: o.Instructions})

	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo text back."},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			return text(in.Text), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "env", Description: "Report cwd and environment."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
			cwd, _ := os.Getwd()
			env := map[string]string{}
			for _, kv := range os.Environ() {
				k, v, _ := strings.Cut(kv, "=")
				env[k] = v
			}
			b, err := json.Marshal(map[string]any{"cwd": cwd, "env": env})
			if err != nil {
				return nil, nil, err
			}
			return text(string(b)), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "sleep", Description: "Sleep ms milliseconds, reporting progress."},
		func(ctx context.Context, req *mcp.CallToolRequest, in sleepIn) (*mcp.CallToolResult, any, error) {
			token := req.Params.GetProgressToken()
			deadline := time.Now().Add(time.Duration(in.MS) * time.Millisecond)
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for n := 1; time.Now().Before(deadline); n++ {
				select {
				case <-ctx.Done():
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "cancelled"}}}, nil, nil
				case <-tick.C:
				}
				if token != nil {
					_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: float64(n)})
				}
			}
			return text(fmt.Sprintf("slept %d", in.MS)), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "crash", Description: "Exit the process with code."},
		func(_ context.Context, _ *mcp.CallToolRequest, in crashIn) (*mcp.CallToolResult, any, error) {
			_, _ = os.Stderr.WriteString("boom\n")
			os.Exit(in.Code)
			return nil, nil, nil
		})
	for i := range o.Extra {
		n := fmt.Sprintf("t%d", i)
		mcp.AddTool(s, &mcp.Tool{Name: n, Description: "Extra tool " + n + "."},
			func(_ context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
				return text(n), nil, nil
			})
	}
	if o.BadSchema {
		s.AddReceivingMiddleware(badSchema)
	}
	return s
}

// badSchema injects a tool with schema {} because Server.AddTool refuses
// non-object schemas, yet real upstreams send them and the gateway must cope.
func badSchema(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method == "tools/call" {
			if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && p.Name == "bad" {
				return text("bad"), nil
			}
		}
		res, err := next(ctx, method, req)
		if method == "tools/list" && err == nil {
			if lr, ok := res.(*mcp.ListToolsResult); ok && lr.NextCursor == "" {
				lr.Tools = append(lr.Tools, &mcp.Tool{Name: "bad", Description: "Tool with a non-object schema.", InputSchema: map[string]any{}})
			}
		}
		return res, err
	}
}
