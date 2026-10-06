package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// openAppWindow shows the UI in a chromeless app window using Chrome, Edge or
// Brave if installed (own profile so it behaves like a separate app);
// otherwise it falls back to the default browser.
func openAppWindow(url string) {
	home, _ := os.UserHomeDir()
	for _, app := range []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Chromium"} {
		for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
			if _, err := os.Stat(filepath.Join(dir, app+".app")); err == nil {
				profile := filepath.Join(dataDir, "window")
				err := exec.Command("open", "-na", filepath.Join(dir, app+".app"), "--args",
					"--app="+url, "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
					"--window-size=1180,780", "--disable-features=Translate").Start()
				if err == nil {
					return
				}
			}
		}
	}
	_ = exec.Command("open", url).Start()
}

func openPath(p string, reveal bool) {
	if reveal {
		_ = exec.Command("open", "-R", p).Start()
		return
	}
	_ = exec.Command("open", p).Start()
}
