//go:build !windows

package main

import (
	"syscall"
	"time"
)

// waitForExit waits until process pid has exited (or the timeout passes).
func waitForExit(pid int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && syscall.Kill(pid, 0) == nil {
		time.Sleep(150 * time.Millisecond)
	}
}
