//go:build !darwin && !windows

package main

import (
	"os/exec"
	"path/filepath"
)

func openAppWindow(url string) {
	for _, b := range []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge"} {
		if p, err := exec.LookPath(b); err == nil {
			if exec.Command(p, "--app="+url, "--user-data-dir="+filepath.Join(dataDir, "window"), "--no-first-run").Start() == nil {
				return
			}
		}
	}
	_ = exec.Command("xdg-open", url).Start()
}

func openPath(p string, reveal bool) {
	if reveal {
		p = filepath.Dir(p)
	}
	_ = exec.Command("xdg-open", p).Start()
}
