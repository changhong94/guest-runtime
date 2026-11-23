package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Ratio-only defaults let multi-GiB microVMs accumulate a large backlog before
// background writeback starts. Bound that trigger for every workload, so IO can
// overlap subsequent computation. This neither preloads data nor changes the
// foreground dirty limit, cache capacity, fsync, or the freeze/sync barrier.
const guestBackgroundWritebackCap = 8 << 20

func configureBackgroundWriteback(read func(string) ([]byte, error), write func(string, []byte) error) error {
	readUint := func(path string) (uint64, error) {
		b, err := read(path)
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	}
	path := "/proc/sys/vm/dirty_background_bytes"
	explicit, err := readUint(path)
	if err != nil {
		return err
	}
	// Preserve an explicit administrator policy, including a lower byte limit.
	if explicit != 0 {
		return nil
	}
	ratio, err := readUint("/proc/sys/vm/dirty_background_ratio")
	if err != nil {
		return err
	}
	if ratio == 0 {
		return nil
	}
	if ratio > 100 {
		return fmt.Errorf("invalid dirty background ratio %d", ratio)
	}
	info, err := read("/proc/meminfo")
	if err != nil {
		return err
	}
	var kib uint64
	for _, line := range strings.Split(string(info), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemTotal:" && fields[2] == "kB" {
			kib, err = strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return err
			}
			break
		}
	}
	if kib == 0 || kib > ^uint64(0)/1024 {
		return fmt.Errorf("invalid guest MemTotal")
	}
	// Do not raise the configured ratio-derived trigger on small guests.
	if (kib*1024/100)*ratio <= guestBackgroundWritebackCap {
		return nil
	}
	if err = write(path, []byte(strconv.FormatUint(guestBackgroundWritebackCap, 10)+"\n")); err != nil {
		return err
	}
	actual, err := readUint(path)
	if err != nil {
		return err
	}
	if actual != guestBackgroundWritebackCap {
		return fmt.Errorf("guest writeback cap not applied: %d", actual)
	}
	return nil
}

func configureGuestWriteback() error {
	return configureBackgroundWriteback(os.ReadFile, func(path string, b []byte) error { return os.WriteFile(path, b, 0644) })
}
