package main

import (
	"encoding/binary"
	"errors"
	"golang.org/x/sys/unix"
	"net"
	"testing"
)

func TestDiagReplies(t *testing.T) {
	message := func(kind uint16, seq uint32, payload []byte) []byte {
		b := make([]byte, 16+len(payload))
		binary.NativeEndian.PutUint32(b, uint32(len(b)))
		binary.NativeEndian.PutUint16(b[4:], kind)
		binary.NativeEndian.PutUint32(b[8:], seq)
		copy(b[16:], payload)
		return b
	}
	data := make([]byte, 72)
	data[0] = unix.AF_INET
	data[4] = 12
	record := message(diagGet, 42, data)
	done := message(unix.NLMSG_DONE, 42, make([]byte, 4))
	records, finished, err := parseDiagReply(append(append([]byte{}, record...), done...), 42)
	if err != nil || !finished || len(records) != 1 || records[0].id[0] != 12 {
		t.Fatalf("%+v %v %v", records, finished, err)
	}
	for _, bad := range [][]byte{record[:12], record[:len(record)-1], message(diagGet, 43, data), message(unix.NLMSG_DONE, 42, nil)} {
		if _, _, err := parseDiagReply(bad, 42); err == nil {
			t.Fatal("accepted malformed diag reply")
		}
	}
	interrupted := append([]byte{}, done...)
	binary.NativeEndian.PutUint16(interrupted[6:], unix.NLM_F_DUMP_INTR)
	if _, _, err := parseDiagReply(interrupted, 42); err == nil {
		t.Fatal("accepted partial dump")
	}
	errno := make([]byte, 4)
	negative := int32(-int(unix.EOPNOTSUPP))
	binary.NativeEndian.PutUint32(errno, uint32(negative))
	if _, _, err := parseDiagReply(message(unix.NLMSG_ERROR, 42, errno), 42); !errors.Is(err, unix.EOPNOTSUPP) {
		t.Fatal(err)
	}
}

func TestForkEntropyRejectsInvalidLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if err := reseedForkKernel(make([]byte, n)); err == nil {
			t.Fatal("accepted invalid seed")
		}
	}
}

func TestLoopbackConnectionIsGuestPrivate(t *testing.T) {
	for _, tc := range []struct {
		a, b    string
		private bool
	}{
		{"127.0.0.1", "127.0.0.2", true}, {"::1", "::1", true},
		{"::ffff:127.0.0.1", "::ffff:127.0.0.2", true},
		{"127.0.0.1", "10.0.0.1", false}, {"10.0.0.1", "10.0.0.1", false},
	} {
		r := tcpIdentity{family: unix.AF_INET6}
		copy(r.id[4:20], net.ParseIP(tc.a).To16())
		copy(r.id[20:36], net.ParseIP(tc.b).To16())
		if r.loopback() != tc.private {
			t.Fatalf("%+v", tc)
		}
		if a, b := net.ParseIP(tc.a).To4(), net.ParseIP(tc.b).To4(); a != nil && b != nil {
			r = tcpIdentity{family: unix.AF_INET}
			copy(r.id[4:8], a)
			copy(r.id[20:24], b)
			if r.loopback() != tc.private {
				t.Fatalf("IPv4 %+v", tc)
			}
		}
	}
}

func TestEmptyForkTCPDoesNotRepeatCompletedDump(t *testing.T) {
	calls := 0
	err := resetInheritedTCPWith(func(seq uint32, kind uint16, family byte, id *[48]byte) ([]tcpIdentity, error) {
		calls++
		if kind != diagGet || id != nil {
			t.Fatal("empty dump caused destruction")
		}
		return nil, nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
func TestForkTCPDestructionStillRequiresVerification(t *testing.T) {
	calls := 0
	err := resetInheritedTCPWith(func(seq uint32, kind uint16, family byte, id *[48]byte) ([]tcpIdentity, error) {
		calls++
		if calls == 1 {
			return []tcpIdentity{{family: 2}}, nil
		}
		if calls == 2 {
			if kind != diagDestroy {
				t.Fatal("expected destruction")
			}
			return nil, nil
		}
		if calls == 3 {
			return []tcpIdentity{{family: 2}}, nil
		} // failed cleanup must fail closed
		t.Fatal("continued after failed verification")
		return nil, nil
	})
	if err == nil || calls != 3 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestForkDiagListenerPolicy(t *testing.T) {
	for _, tc := range []struct {
		local, keep string
		valid       bool
	}{
		{"0.0.0.0", "", true}, {"127.0.0.1", "", true}, {"192.168.0.2", "192.168.0.2", true},
		{"192.168.0.2", "192.168.0.3", false}, {"192.168.0.2", "", false},
		{"::", "", true}, {"::1", "", true}, {"2001:db8::2", "2001:db8::2", true}, {"2001:db8::2", "", false},
	} {
		t.Run(tc.local+"/"+tc.keep, func(t *testing.T) {
			r := tcpIdentity{family: unix.AF_INET6, state: 10}
			ip := net.ParseIP(tc.local)
			if v4 := ip.To4(); v4 != nil {
				r.family = unix.AF_INET
				copy(r.id[4:8], v4)
			} else {
				copy(r.id[4:20], ip.To16())
			}
			calls := 0
			err := resetInheritedTCPWith(func(_ uint32, kind uint16, family byte, _ *[48]byte) ([]tcpIdentity, error) {
				calls++
				if kind != diagGet {
					t.Fatal("destroyed a listener")
				}
				if family == r.family {
					return []tcpIdentity{r}, nil
				}
				return nil, nil
			}, net.ParseIP(tc.keep))
			if (err == nil) != tc.valid {
				t.Fatalf("policy mismatch: %v", err)
			}
			if tc.valid && calls != 2 {
				t.Fatalf("unnecessary redump: %d", calls)
			}
		})
	}
}

func TestForkDiagRejectListenerBeforeDestroy(t *testing.T) {
	listener := tcpIdentity{family: unix.AF_INET, state: 10}
	copy(listener.id[4:8], net.ParseIP("192.168.0.2").To4())
	err := resetInheritedTCPWith(func(_ uint32, kind uint16, _ byte, _ *[48]byte) ([]tcpIdentity, error) {
		if kind != diagGet {
			t.Fatal("mutated connections before listener validation")
		}
		return []tcpIdentity{{family: unix.AF_INET}, listener}, nil
	})
	if err == nil {
		t.Fatal("accepted stale listener identity")
	}
}
