package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// applyUpdateFile unpacks the new OfficeChat.app next to the current one and
// swaps them. A running app can be moved on macOS; the old copy is removed
// on the next start.
func applyUpdateFile(zip string) (func(background bool) error, error) {
	app := appBundlePath()
	if app == "" {
		return nil, errors.New("not running from OfficeChat.app")
	}
	if strings.Contains(app, "/AppTranslocation/") || strings.HasPrefix(app, "/Volumes/") {
		return nil, errors.New("move OfficeChat to the Applications folder to get automatic updates")
	}
	parent := filepath.Dir(app)
	tmp := filepath.Join(parent, ".OfficeChat-update")
	old := filepath.Join(parent, ".OfficeChat-old.app")
	_ = os.RemoveAll(tmp)
	_ = os.RemoveAll(old)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, errors.New("no permission to update " + parent)
	}
	defer os.RemoveAll(tmp)
	if out, err := exec.Command("ditto", "-x", "-k", zip, tmp).CombinedOutput(); err != nil {
		return nil, errors.New("unpack failed: " + strings.TrimSpace(string(out)))
	}
	newApp := filepath.Join(tmp, "OfficeChat.app")
	if _, err := os.Stat(filepath.Join(newApp, "Contents", "MacOS", "OfficeChat")); err != nil {
		return nil, errors.New("update package is incomplete")
	}
	_ = exec.Command("xattr", "-dr", "com.apple.quarantine", newApp).Run()
	if err := os.Rename(app, old); err != nil {
		return nil, err
	}
	if err := os.Rename(newApp, app); err != nil {
		_ = os.Rename(old, app)
		return nil, err
	}
	return func(background bool) error {
		args := []string{"-n", app}
		// `open` doesn't pass our environment on; keep test overrides.
		for _, k := range []string{"OFFICECHAT_DATA", "OFFICECHAT_UPDATE_URL"} {
			if v := os.Getenv(k); v != "" {
				args = append(args, "--env", k+"="+v)
			}
		}
		args = append(args, "--args", "-waitpid", strconv.Itoa(os.Getpid()))
		if background {
			args = append([]string{"-g"}, args...)
			args = append(args, "-background")
		}
		return exec.Command("open", args...).Start()
	}, nil
}

func cleanupOldVersion() {
	if app := appBundlePath(); app != "" {
		_ = os.RemoveAll(filepath.Join(filepath.Dir(app), ".OfficeChat-old.app"))
	}
}
