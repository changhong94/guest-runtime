package main

import (
	"errors"
	"testing"
	"time"
)

func TestJoinForkIdentityOverlapsAndWaitsOnFailure(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	failure := errors.New("socket failure")
	go func() {
		finished <- joinForkIdentity(func() error { <-started; return failure }, func() error { close(started); <-release; return nil })
	}()
	<-started
	select {
	case <-finished:
		t.Fatal("returned before worker joined")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-finished:
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("join stuck")
	}
}
func TestJoinForkIdentityRejectsEntropyFailure(t *testing.T) {
	failure := errors.New("reseed failure")
	if err := joinForkIdentity(func() error { return nil }, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
