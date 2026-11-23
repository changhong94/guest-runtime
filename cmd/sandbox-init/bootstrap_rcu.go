package main

import (
	"errors"
	"os"
	"strings"
)

// The inner init moves before any workload or plugin is released. Expedite
// that guest-only RCU grace period without dropping it, then restore the
// original policy before acknowledging the bootstrap barrier.
func bootstrapRCU(operation func() error) error {
	return bootstrapRCUWith(os.ReadFile, os.WriteFile, operation)
}

func bootstrapRCUWith(read func(string) ([]byte, error), write func(string, []byte, os.FileMode) error, operation func() error) (err error) {
	const path = "/sys/kernel/rcu_expedited"
	previous, e := read(path)
	if e != nil || strings.TrimSpace(string(previous)) != "0" {
		return operation()
	}
	if e = write(path, []byte("1"), 0); e != nil {
		return operation()
	}
	defer func() { err = errors.Join(err, write(path, previous, 0)) }()
	return operation()
}
