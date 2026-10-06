//go:build !windows && !darwin

package main

import "errors"

func applyUpdateFile(string) (func(bool) error, error) {
	return nil, errors.New("automatic updates are only available on Windows and macOS")
}

func cleanupOldVersion() {}
