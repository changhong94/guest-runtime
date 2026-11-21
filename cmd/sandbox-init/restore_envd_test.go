package main

import (
	"encoding/json"
	"github.com/kuasar-sandbox/guest-runtime/pkg/guestabi/proto"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRestoreEnvdInitialization(t *testing.T) {
	for _, status := range []int{204, 401, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/init" {
					t.Error("wrong init request")
				}
				var v map[string]any
				if json.NewDecoder(r.Body).Decode(&v) != nil || v["defaultUser"] != "user" {
					t.Error("init payload lost")
				}
				w.WriteHeader(status)
				w.Write([]byte("private-secret"))
			}))
			defer s.Close()
			err := initializeRestoredEnvdAt(json.RawMessage(`{"defaultUser":"user"}`), s.URL+"/init")
			if (err == nil) != (status == 204) {
				t.Fatalf("status %d error %v", status, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-secret") {
				t.Fatal("response leaked")
			}
		})
	}
	for _, body := range []string{"", "invalid", strings.Repeat("x", (512<<10)+1)} {
		if initializeRestoredEnvd(json.RawMessage(body)) == nil {
			t.Fatal("accepted invalid init")
		}
	}
}

func TestInvalidRestoreInitCannotPublishAck(t *testing.T) {
	_, sup, _ := usageReattachFixture(t)
	g, h := usageSocketPair(t)
	if handed, err := reattachExecutionInitAck(g, sup, 1, proto.TypeRestoreAck, json.RawMessage(`invalid`)); err == nil || handed {
		t.Fatal("failed initialization accepted")
	}
	h.SetDeadline(time.Now().Add(20 * time.Millisecond))
	if msg, err := proto.ReadMessage(h); err == nil {
		t.Fatalf("readiness escaped failure: %+v", msg)
	}
}
func TestForkRestoreUsesLifecycleBarrier(t *testing.T) {
	g, h := usageSocketPair(t)
	managementLifecycle.Lock()
	locked := true
	defer func() {
		if locked {
			managementLifecycle.Unlock()
		}
	}()
	done := make(chan struct{})
	go func() { handleReverseConn(g, nil); close(done) }()
	if err := proto.WriteMessage(h, &proto.Message{Type: proto.TypeForkRestore}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("fork_restore bypassed lifecycle barrier")
	case <-time.After(20 * time.Millisecond):
	}
	managementLifecycle.Unlock()
	locked = false
	if msg, err := proto.ReadMessage(h); err != nil || msg.Type != proto.TypeError {
		t.Fatalf("expected no-workload rejection after barrier: %+v %v", msg, err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not finish")
	}
}

func TestRestoredEnvdUnixTransport(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "init.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/init" {
			t.Error("wrong request")
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go s.Serve(l)
	defer s.Close()
	if err := initializeRestoredEnvdTransport(json.RawMessage(`{"defaultUser":"user"}`), "http://localhost/init", socket); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("request replayed")
	}
	s.Close()
	if err := initializeRestoredEnvdTransport(json.RawMessage(`{}`), "http://127.0.0.1:1/init", socket); err == nil {
		t.Fatal("unavailable Unix endpoint accepted")
	}
}
