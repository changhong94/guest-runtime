package main

import (
	"errors"
	"testing"
	"time"
)

func TestForkSocketIdentityJoinsBothFailuresBeforeReturn(t *testing.T) {
	udpErr, tcpErr := errors.New("udp failure"), errors.New("tcp failure")
	udpStarted, releaseUDP, tcpDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- checkForkSocketIdentity(func() error { close(udpStarted); <-releaseUDP; return udpErr }, func() error { <-udpStarted; close(tcpDone); return tcpErr })
	}()
	<-tcpDone
	select {
	case err := <-done:
		t.Fatalf("returned before UDP completed: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(releaseUDP)
	select {
	case err := <-done:
		if !errors.Is(err, udpErr) || !errors.Is(err, tcpErr) {
			t.Fatalf("lost failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not join UDP")
	}
}
