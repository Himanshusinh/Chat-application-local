//go:build !windows

package main

import "errors"

const canFixFirewall = false

func fixFirewall() error { return errors.New("only needed on Windows") }
