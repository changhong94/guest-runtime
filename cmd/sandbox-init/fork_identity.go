package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const diagGet = 20
const diagDestroy = 21
const connectedTCP = uint32(0xfff & ^((1 << 7) | (1 << 10)))

type tcpIdentity struct {
	family byte
	id     [48]byte
}

// Loopback peers are entirely inside the copied guest. Breaking them would
// corrupt inherited IPC without providing any external identity isolation.
func (r tcpIdentity) loopback() bool {
	n := 16
	if r.family == unix.AF_INET {
		n = 4
	}
	return net.IP(r.id[4:4+n]).IsLoopback() && net.IP(r.id[20:20+n]).IsLoopback()
}

// parseDiagReply rejects truncated, interrupted, wrong-sequence and kernel-error
// responses. It never interprets a partial dump as successful cleanup.
func parseDiagReply(buf []byte, seq uint32) (records []tcpIdentity, done bool, err error) {
	for len(buf) > 0 {
		if len(buf) < 16 {
			return nil, false, fmt.Errorf("short SOCK_DIAG header")
		}
		size := int(binary.NativeEndian.Uint32(buf))
		kind := binary.NativeEndian.Uint16(buf[4:])
		flags := binary.NativeEndian.Uint16(buf[6:])
		if size < 16 || size > len(buf) || binary.NativeEndian.Uint32(buf[8:]) != seq || flags&unix.NLM_F_DUMP_INTR != 0 {
			return nil, false, fmt.Errorf("invalid SOCK_DIAG reply")
		}
		payload := buf[16:size]
		switch kind {
		case unix.NLMSG_ERROR, unix.NLMSG_DONE:
			if len(payload) < 4 {
				return nil, false, fmt.Errorf("short SOCK_DIAG completion")
			}
			code := int32(binary.NativeEndian.Uint32(payload))
			if code != 0 {
				return nil, false, unix.Errno(-code)
			}
			done = true
		case diagGet:
			if len(payload) < 72 || (payload[0] != unix.AF_INET && payload[0] != unix.AF_INET6) {
				return nil, false, fmt.Errorf("invalid SOCK_DIAG record")
			}
			record := tcpIdentity{family: payload[0]}
			copy(record.id[:], payload[4:52])
			records = append(records, record)
		default:
			return nil, false, fmt.Errorf("unexpected SOCK_DIAG type %d", kind)
		}
		aligned := (size + 3) &^ 3
		if aligned > len(buf) {
			return nil, false, fmt.Errorf("truncated SOCK_DIAG padding")
		}
		buf = buf[aligned:]
	}
	return records, done, nil
}

func diagRequest(fd int, seq uint32, kind uint16, family byte, id *[48]byte) ([]tcpIdentity, error) {
	req := make([]byte, 72)
	binary.NativeEndian.PutUint32(req, uint32(len(req)))
	binary.NativeEndian.PutUint16(req[4:], kind)
	flags := uint16(unix.NLM_F_REQUEST | unix.NLM_F_DUMP)
	if kind == diagDestroy {
		flags = unix.NLM_F_REQUEST | unix.NLM_F_ACK
	}
	binary.NativeEndian.PutUint16(req[6:], flags)
	binary.NativeEndian.PutUint32(req[8:], seq)
	req[16] = family
	req[17] = unix.IPPROTO_TCP
	binary.NativeEndian.PutUint32(req[20:], connectedTCP)
	if id != nil {
		copy(req[24:], id[:])
	}
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	var records []tcpIdentity
	for {
		buf := make([]byte, 65536)
		n, sender, err := unix.Recvfrom(fd, buf, unix.MSG_TRUNC)
		if err != nil {
			return nil, err
		}
		peer, ok := sender.(*unix.SockaddrNetlink)
		if n > len(buf) || !ok || peer.Pid != 0 {
			return nil, fmt.Errorf("truncated or nonkernel SOCK_DIAG reply")
		}
		batch, done, err := parseDiagReply(buf[:n], seq)
		if err != nil {
			return nil, err
		}
		records = append(records, batch...)
		if done {
			return records, nil
		}
	}
}

// resetInheritedTCP operates only in the guest while the workload is frozen and
// the child's host TAP is DOWN. It preserves listeners, and never sends resets
// to the parent's peer through the gated child link. Unsupported kernels fail.
func resetInheritedTCP() error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err = unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	if err = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 2}); err != nil {
		return err
	}
	seq := uint32(0)
	for _, family := range []byte{unix.AF_INET, unix.AF_INET6} {
		seq++
		records, err := diagRequest(fd, seq, diagGet, family, nil)
		if family == unix.AF_INET6 && errors.Is(err, unix.EAFNOSUPPORT) {
			continue
		}
		if err != nil {
			return fmt.Errorf("TCP diag: %w", err)
		}
		for _, record := range records {
			if record.loopback() {
				continue
			}
			seq++
			_, err = diagRequest(fd, seq, diagDestroy, record.family, &record.id)
			if err != nil && !errors.Is(err, unix.ENOENT) {
				return fmt.Errorf("TCP destroy: %w", err)
			}
		}
		seq++
		remaining, err := diagRequest(fd, seq, diagGet, family, nil)
		if err != nil {
			return err
		}
		for _, record := range remaining {
			if !record.loopback() {
				return fmt.Errorf("TCP cleanup left external socket")
			}
		}
	}
	return nil
}

// reseedForkKernel injects host-generated entropy and forces a CRNG reseed
// before guest code resumes. Application PRNG state remains application-owned.
func reseedForkKernel(seed []byte) error {
	if len(seed) != 32 {
		return fmt.Errorf("fork requires 32 bytes of host entropy")
	}
	f, err := os.OpenFile("/dev/urandom", os.O_WRONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR || stat.Rdev != unix.Mkdev(1, 9) {
		return fmt.Errorf("invalid kernel entropy device")
	}
	arg := struct {
		Bits int32
		Size int32
		Data [32]byte
	}{Bits: 256, Size: 32}
	copy(arg.Data[:], seed)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.RNDADDENTROPY, uintptr(unsafe.Pointer(&arg))); errno != 0 {
		return errno
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.RNDRESEEDCRNG, 0); errno != 0 {
		return errno
	}
	return nil
}
