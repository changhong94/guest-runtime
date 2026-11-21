package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"sync/atomic"
	"time"
)

const localInitSocket = "/run/kuasar-envd-init/init.sock"

// Both endpoints are inside the VM. An idle Unix stream can be snapshotted
// without inheriting any external network connection or old credentials.
var preparedInitTransport = newLocalInitTransport(localInitSocket)

func prepareLocalInitConnection() error {
	if _, err := os.Lstat(localInitSocket); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// HEAD is rejected without applying initialization; this only primes transport.
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "http://localhost/init", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: preparedInitTransport}).Do(req)
	if err != nil {
		return fmt.Errorf("prepare init connection: %w", err)
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNotFound {
		return errors.New("unexpected local init probe")
	}
	return nil
}

// Runs in the inner executor's network namespace after identity renewal and
// thaw. No interpreter/tenant command is launched; the ordinary envd handler
// still validates and applies the complete init request before readiness.
func initializeRestoredEnvd(body json.RawMessage) error {
	if len(body) == 0 || len(body) > 512<<10 || !json.Valid(body) {
		return errors.New("invalid restored envd initialization")
	}
	const socket = localInitSocket
	if st, err := os.Lstat(socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return errors.New("invalid local init socket")
		}
		return initializeRestoredEnvdTransport(body, "http://localhost/init", socket)
	} else if !os.IsNotExist(err) {
		return errors.New("inspect local init socket")
	}
	// Compatibility only: never replay a failed or rejected Unix request.
	return initializeRestoredEnvdAt(body, "http://127.0.0.1:49983/init")
}

func initializeRestoredEnvdAt(body json.RawMessage, endpoint string) error {
	return initializeRestoredEnvdTransport(body, endpoint, "")
}
func initializeRestoredEnvdTransport(body json.RawMessage, endpoint, socket string) error {
	started := time.Now()
	var mono unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &mono)
	traceMono := mono.Nano()
	var connected, written, firstByte atomic.Int64
	var reused atomic.Bool
	defer func() {
		if firstByte.Load() > 0 {
			logf("restore stages envd_start_mono_ns=%d envd_connected_ns=%d envd_written_ns=%d envd_first_byte_ns=%d envd_done_ns=%d envd_reused=%t", traceMono, connected.Load(), written.Load(), firstByte.Load(), time.Since(started).Nanoseconds(), reused.Load())
		}
	}()
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		reused.Store(info.Reused)
		connected.Store(time.Since(started).Nanoseconds())
	}, WroteRequest: func(httptrace.WroteRequestInfo) { written.Store(time.Since(started).Nanoseconds()) }, GotFirstResponseByte: func() { firstByte.Store(time.Since(started).Nanoseconds()) }}
	ctx, cancel := context.WithTimeout(httptrace.WithClientTrace(context.Background(), trace), 10*time.Second)
	defer cancel()
	fallback := &http.Transport{}
	var tr http.RoundTripper = fallback
	if socket != "" {
		fallback.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
	}
	if socket == localInitSocket {
		tr = preparedInitTransport
	} else {
		defer fallback.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("construct restored envd initialization")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kuasar-Init-Timing", "1")
	response, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return errors.New("restored envd initialization transport failed")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return errors.New("restored envd initialization rejected")
	}
	return nil
}
