package main

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestBootstrapRCURestoresPolicyOnFailure(t *testing.T) {
	failure := errors.New("migration failed")
	var events []string
	err := bootstrapRCUWith(func(string) ([]byte, error) { return []byte("0\n"), nil }, func(_ string, b []byte, _ os.FileMode) error { events = append(events, string(b)); return nil }, func() error { events = append(events, "barrier"); return failure })
	if !errors.Is(err, failure) || !reflect.DeepEqual(events, []string{"1", "barrier", "0\n"}) {
		t.Fatalf("events=%q error=%v", events, err)
	}
}
func TestBootstrapRCURestoreFailureRejectsBootstrap(t *testing.T) {
	failure := errors.New("restore failed")
	calls := 0
	err := bootstrapRCUWith(func(string) ([]byte, error) { return []byte("0"), nil }, func(_ string, b []byte, _ os.FileMode) error {
		if string(b) == "0" {
			return failure
		}
		return nil
	}, func() error { calls++; return nil })
	if calls != 1 || !errors.Is(err, failure) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
func TestBootstrapRCUNeverSkipsMigration(t *testing.T) {
	for _, policy := range []string{"1", "unsupported", "denied"} {
		t.Run(policy, func(t *testing.T) {
			calls, writes := 0, 0
			err := bootstrapRCUWith(func(string) ([]byte, error) {
				if policy == "unsupported" {
					return nil, os.ErrNotExist
				}
				if policy == "denied" {
					return []byte("0"), nil
				}
				return []byte(policy), nil
			}, func(string, []byte, os.FileMode) error { writes++; return os.ErrPermission }, func() error { calls++; return nil })
			if err != nil || calls != 1 || policy != "denied" && writes != 0 {
				t.Fatalf("calls=%d writes=%d err=%v", calls, writes, err)
			}
		})
	}
}
