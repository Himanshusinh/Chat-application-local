package main

import (
	"io"
	"os"
	"os/exec"
	"strconv"
)

// applyUpdateFile swaps in the new exe. Windows can't overwrite a running
// exe but can rename it, so the old one becomes OfficeChat.exe.old and is
// deleted on the next start. The installer gives users write access to the
// install folder, so this needs no admin prompt.
func applyUpdateFile(newExe string) (func(background bool) error, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return nil, err
	}
	if err := copyFile(newExe, exe); err != nil {
		_ = os.Remove(exe)
		_ = os.Rename(old, exe) // put the working version back
		return nil, err
	}
	return func(background bool) error {
		args := []string{"-waitpid", strconv.Itoa(os.Getpid())}
		if background {
			args = append(args, "-background")
		}
		return exec.Command(exe, args...).Start()
	}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func cleanupOldVersion() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}
