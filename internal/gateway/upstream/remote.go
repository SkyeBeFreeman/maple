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
		// No standalone SSE GET: the SDK waits for its response headers
		// inside Connect, outside ctx, and some gateways hold them for
		// minutes. Its only loss, unannounced tool changes, is made up for by
		// relisting on reconnect in the gateway.
		t = &mcp.StreamableClientTransport{Endpoint: s.URL, HTTPClient: hc, DisableStandaloneSSE: true}
	}
	release := func(cs *mcp.ClientSession) {
		if cs != nil {
			_ = cs.Close()
		}
		if sse != nil {
			sse.close()
		}
	}
	cs, err := connectWithin(ctx, func() (*mcp.ClientSession, error) { return newClient(h).Connect(ctx, t, nil) }, release)
	if err != nil {
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

// connectWithin returns by the time ctx ends even if connect does not: the
// SDK has had handshake paths that ignore ctx, and a stuck Dial would hold a
// pool start for the whole call timeout. release gets the session of a
// failed connect (nil) or of one that finished after Dial gave up.
func connectWithin(ctx context.Context, connect func() (*mcp.ClientSession, error), release func(*mcp.ClientSession)) (*mcp.ClientSession, error) {
	type result struct {
		cs  *mcp.ClientSession
		err error
	}
	// Unbuffered, so that exactly one side owns the outcome: either Dial
	// receives it, or the goroutine sees abandoned and releases it.
	done := make(chan result)
	abandoned := make(chan struct{})
	go func() {
		cs, err := connect()
		select {
		case done <- result{cs, err}:
		case <-abandoned:
			release(cs)
		}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			release(r.cs)
			return nil, r.err
		}
		return r.cs, nil
	case <-ctx.Done():
		close(abandoned)
		return nil, ctx.Err()
	}
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
