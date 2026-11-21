package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"time"
)

// A single private initialization stream is prepared while quiescing. Perform
// its request synchronously instead of waking HTTP transport reader/writer
// goroutines after restore. This still uses the ordinary HTTP handler/auth.
// Never replay an initialization whose request may have reached the server.
type localInitTransport struct {
	socket string
	gate   chan struct{}
	conn   net.Conn
	reader *bufio.Reader
}

func newLocalInitTransport(socket string) *localInitTransport {
	return &localInitTransport{socket: socket, gate: make(chan struct{}, 1)}
}
func (t *localInitTransport) closeLocked() {
	if t.conn != nil {
		t.conn.Close()
	}
	t.conn = nil
	t.reader = nil
}
func (t *localInitTransport) CloseIdleConnections() {
	t.gate <- struct{}{}
	defer func() { <-t.gate }()
	t.closeLocked()
}
func (t *localInitTransport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	ctx := req.Context()
	select {
	case t.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-t.gate }()
	if req.Body != nil {
		defer req.Body.Close()
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	reused := t.conn != nil
	if t.conn == nil {
		t.conn, err = (&net.Dialer{}).DialContext(ctx, "unix", t.socket)
		if err != nil {
			return nil, err
		}
		t.reader = bufio.NewReader(t.conn)
	}
	defer func() {
		if err != nil {
			t.closeLocked()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = t.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	conn := t.conn
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
		if t.conn != nil {
			t.conn.SetDeadline(time.Time{})
		}
	}()
	trace := httptrace.ContextClientTrace(ctx)
	if trace != nil && trace.GotConn != nil {
		trace.GotConn(httptrace.GotConnInfo{Conn: conn, Reused: reused})
	}
	err = req.Write(conn)
	if trace != nil && trace.WroteRequest != nil {
		trace.WroteRequest(httptrace.WroteRequestInfo{Err: err})
	}
	if err != nil {
		return nil, err
	}
	if _, err = t.reader.Peek(1); err != nil {
		return nil, err
	}
	if trace != nil && trace.GotFirstResponseByte != nil {
		trace.GotFirstResponseByte()
	}
	resp, err = http.ReadResponse(t.reader, req)
	if err != nil {
		return nil, err
	}
	// Initialization responses are empty or small errors. Bound draining, keep
	// the connection exclusively owned until the complete response is consumed.
	n, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 512*1024+1))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if n > 512*1024 {
		return nil, errors.New("oversized local init response")
	}
	resp.Body = http.NoBody
	if resp.Close {
		t.closeLocked()
	}
	return resp, nil
}
