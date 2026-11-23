package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestBackgroundWritebackPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, bytes, ratio, total string
		want                      bool
	}{
		{"large-default", "0", "10", "2097152", true},
		{"small-guest", "0", "10", "32768", false},
		{"explicit-lower", "1048576", "0", "2097152", false},
		{"explicit-higher", "16777216", "0", "2097152", false},
		{"disabled-ratio", "0", "0", "2097152", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{"/proc/sys/vm/dirty_background_bytes": []byte(tc.bytes), "/proc/sys/vm/dirty_background_ratio": []byte(tc.ratio), "/proc/meminfo": []byte("MemTotal: " + tc.total + " kB\n")}
			writes := 0
			err := configureBackgroundWriteback(func(p string) ([]byte, error) {
				b, ok := files[p]
				if !ok {
					return nil, fmt.Errorf("unexpected read %s", p)
				}
				return b, nil
			}, func(p string, b []byte) error {
				if p != "/proc/sys/vm/dirty_background_bytes" {
					t.Fatalf("changed foreground policy: %s", p)
				}
				writes++
				files[p] = b
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if (writes == 1) != tc.want {
				t.Fatalf("writes=%d want changed=%t", writes, tc.want)
			}
		})
	}
}
func TestBackgroundWritebackFailureIsVisible(t *testing.T) {
	sentinel := errors.New("denied")
	err := configureBackgroundWriteback(func(p string) ([]byte, error) { return nil, sentinel }, func(string, []byte) error { t.Fatal("unexpected write"); return nil })
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
