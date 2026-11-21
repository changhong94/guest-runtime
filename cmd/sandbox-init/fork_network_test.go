package main

import (
	"github.com/kuasar-sandbox/guest-runtime/pkg/guestabi/proto"
	"net"
	"testing"
)

func TestForkNetworkPreflight(t *testing.T) {
	for _, spec := range []*proto.NetworkSpec{nil, {}, {MAC: "ff:ff:ff:ff:ff:ff", IPCIDR: "10.0.0.2/24"}, {MAC: "02:aa:00:00:00:01"}, {MAC: "00:00:00:00:00:00", IPCIDR: "10.0.0.2/24"}} {
		if err := validateForkNetwork(spec); err == nil {
			t.Fatalf("accepted %+v", spec)
		}
	}
	if err := validateForkNetwork(&proto.NetworkSpec{MAC: "02:aa:00:00:00:02", IPCIDR: "10.0.0.2/24"}); err != nil {
		t.Fatal(err)
	}
}

func TestForkListenerPolicy(t *testing.T) {
	empty := func(string) ([]byte, error) { return []byte("sl local_address rem_address st\n"), nil }
	if err := validateInheritedListeners(empty); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		table, record string
		valid         bool
	}{
		{"tcp", "0: 00000000:1234 00000000:0000 0A", true},
		{"tcp", "0: 0200A8C0:1234 0300A8C0:1234 01", true},
		{"tcp", "0: 0200A8C0:1234 00000000:0000 0A", false},
		{"tcp6", "0: 00000000000000000000000000000000:1234 00000000000000000000000000000000:0000 0A", true},
		{"udp", "0: 00000000:1234 00000000:0000 07", true},
		{"udp", "0: 0200A8C0:1234 0300A8C0:1234 01", false},
		{"udp", "0: 0200A8C0:1234 00000000:0000 07", false},
		{"tcp", "0: 0100007F:1234 00000000:0000 0A", true},
		{"udp", "0: 0100007F:1234 0100007F:1235 01", true},
		{"tcp", "bad", false},
	} {
		read := func(path string) ([]byte, error) {
			if path == "/proc/net/"+tc.table {
				return []byte("sl local_address rem_address st\n" + tc.record + "\n"), nil
			}
			return empty(path)
		}
		if err := validateInheritedListeners(read); (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestPreservedAddressListener(t *testing.T) {
	read := func(path string) ([]byte, error) {
		s := "sl local_address rem_address st\n"
		if path == "/proc/net/tcp" {
			s += "0: 0200A8C0:1234 00000000:0000 0A\n"
		}
		return []byte(s), nil
	}
	if err := validateInheritedListeners(read, net.ParseIP("192.168.0.2")); err != nil {
		t.Fatal(err)
	}
	if err := validateInheritedListeners(read, net.ParseIP("192.168.0.3")); err == nil {
		t.Fatal("kept changed IP")
	}
}

func TestConnectedUDPPreservedAddress(t *testing.T) {
	read := func(path string) ([]byte, error) {
		s := "sl local_address rem_address st\n"
		if path == "/proc/net/udp" {
			s += "0: 0200A8C0:1234 0300A8C0:4321 01\n"
		}
		return []byte(s), nil
	}
	if err := validateInheritedListeners(read, net.ParseIP("192.168.0.2")); err != nil {
		t.Fatal(err)
	}
	if err := validateInheritedListeners(read, net.ParseIP("192.168.0.4")); err == nil {
		t.Fatal("accepted stale local address")
	}
	if err := validateInheritedListeners(read); err == nil {
		t.Fatal("accepted external socket without retained address")
	}
}
