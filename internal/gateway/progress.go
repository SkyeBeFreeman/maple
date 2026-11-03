package gateway

import (
	"context"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// progressBuffer bounds notifications queued for one call; beyond it
	// they are dropped, since a hook must never block.
	progressBuffer = 64
	// notifyTimeout bounds one forwarded notification, so a stalled agent
	// stream cannot hold up the end of the call for long.
	notifyTimeout = time.Second
)

// trackProgress swaps the agent's progress token for one unique across the
// gateway, since tokens of different agents may collide on a shared
// upstream. Notifications are relayed in order by one goroutine using the
// call's ctx, which routes them onto that request's response stream; done
// waits for it so that none trails the result.
func (g *Gateway) trackProgress(ctx context.Context, ss *mcp.ServerSession, token any) (string, func()) {
	ch := make(chan *mcp.ProgressNotificationParams, progressBuffer)
	g.progMu.Lock()
	g.progSeq++
	ours := "maple-" + strconv.FormatUint(g.progSeq, 10)
	g.progress[ours] = ch
	g.progMu.Unlock()

	relayed := make(chan struct{})
	go func() {
		defer close(relayed)
		for p := range ch {
			if ss == nil {
				continue
			}
			p.ProgressToken = token
			nctx, cancel := context.WithTimeout(ctx, notifyTimeout)
			_ = ss.NotifyProgress(nctx, p)
			cancel()
		}
	}()
	return ours, func() {
		g.progMu.Lock()
		delete(g.progress, ours)
		close(ch)
		g.progMu.Unlock()
		<-relayed
	}
}

// forwardProgress is an upstream hook. It sends under progMu so that it
// never races with the close in trackProgress's done.
func (g *Gateway) forwardProgress(p *mcp.ProgressNotificationParams) {
	key, ok := p.ProgressToken.(string)
	if !ok {
		return
	}
	np := *p
	g.progMu.Lock()
	defer g.progMu.Unlock()
	if ch, ok := g.progress[key]; ok {
		select {
		case ch <- &np:
		default:
		}
	}
}
