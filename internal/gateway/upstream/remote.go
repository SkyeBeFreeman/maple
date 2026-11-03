package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

// headerTransport adds fixed headers to every request, including the
// requests the SDK issues on its own (SSE GET, session DELETE).
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t *headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify the caller's request.
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}

// sseTransport keeps the SSE stream alive past Dial: the SDK opens the GET
// with the Connect context, so a Dial timeout would end the stream as soon as
// Dial returns. ctx still bounds the connect phase itself.
type sseTransport struct {
	mcp.SSEClientTransport
	cancel context.CancelFunc
}

func (t *sseTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	c, err := t.SSEClientTransport.Connect(streamCtx)
	if !stop() || err != nil {
		cancel()
		if err == nil {
			_ = c.Close()
			err = ctx.Err()
		}
		return nil, err
	}
	t.cancel = cancel
	return c, nil
}

func (t *sseTransport) close() {
	if t.cancel != nil {
		t.cancel()
	}
}

type remoteSession struct {
	*session
	sse *sseTransport
}

func dialRemote(ctx context.Context, s Spec, h Hooks) (Session, error) {
	hc := &http.Client{Transport: &headerTransport{headers: s.Headers, base: http.DefaultTransport}}
	var t mcp.Transport
	var sse *sseTransport
	if s.Transport == config.TransportSSE {
		sse = &sseTransport{SSEClientTransport: mcp.SSEClientTransport{Endpoint: s.URL, HTTPClient: hc}}
		t = sse
	} else {
		t = &mcp.StreamableClientTransport{Endpoint: s.URL, HTTPClient: hc}
	}
	cs, err := newClient(h).Connect(ctx, t, nil)
	if err != nil {
		if sse != nil {
			sse.close()
		}
		return nil, fmt.Errorf("connect %s: %w", s.URL, err)
	}
	rs := &remoteSession{session: newSession(cs), sse: sse}
	rs.watch(func(err error) error {
		if rs.sse != nil {
			rs.sse.close()
		}
		if err == nil || errors.Is(err, io.EOF) {
			return fmt.Errorf("connection to %s closed", s.URL)
		}
		return fmt.Errorf("connection to %s lost: %w", s.URL, err)
	})
	return rs, nil
}

func (s *remoteSession) PID() int { return 0 }

func (s *remoteSession) Close() error {
	s.closeOnce.Do(func() {
		s.beginClose()
		if err := s.cs.Close(); err != nil && !errors.Is(err, mcp.ErrConnectionClosed) && !errors.Is(err, io.EOF) {
			s.closeErr = fmt.Errorf("close: %w", err)
		}
		<-s.done
	})
	return s.closeErr
}
