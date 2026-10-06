//go:build !darwin && !windows

package main

func isInstalled() bool       { return false }
func setAutoStart(bool) error { return nil }
