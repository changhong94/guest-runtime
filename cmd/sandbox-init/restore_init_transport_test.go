package main

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalInitTransportReuseAndNoReplay(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "init.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls, conns atomic.Int64
	s := &http.Server{ConnState: func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 3 {
			c, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				c.Close()
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go s.Serve(l)
	defer s.Close()
	tr := newLocalInitTransport(socket)
	defer tr.CloseIdleConnections()
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("POST", "http://localhost/init", strings.NewReader("{}"))
		resp, e := tr.RoundTrip(req)
		if e != nil || resp.StatusCode != 204 {
			t.Fatalf("%v %v", resp, e)
		}
	}
	if conns.Load() != 1 {
		t.Fatal("connection not reused")
	}
	req, _ := http.NewRequest("POST", "http://localhost/init", strings.NewReader("{}"))
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("accepted closed response")
	}
	if calls.Load() != 3 {
		t.Fatal("failed init replayed")
	}
}
func TestLocalInitTransportCancellation(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "init.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release; w.WriteHeader(204) })}
	go s.Serve(l)
	defer s.Close()
	defer close(release)
	tr := newLocalInitTransport(socket)
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://localhost/init", strings.NewReader("{}"))
	done := make(chan error, 1)
	go func() { _, e := tr.RoundTrip(req); done <- e }()
	<-started
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("accepted canceled init")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt read")
	}
}
