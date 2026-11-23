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
