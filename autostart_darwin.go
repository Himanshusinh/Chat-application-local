package main

import (
	"os"
	"path/filepath"
	"strings"
)

func appBundlePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
		return exe[:i+4]
	}
	return ""
}

func isInstalled() bool { return strings.HasPrefix(appBundlePath(), "/Applications/") }

// setAutoStart installs/removes a LaunchAgent that opens the app hidden at login.
func setAutoStart(on bool) error {
	home, _ := os.UserHomeDir()
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.officechat.app.plist")
	app := appBundlePath()
	if !on || app == "" {
		if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(plist), 0o755)
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.officechat.app</string>
  <key>ProgramArguments</key><array>
    <string>/usr/bin/open</string><string>-g</string><string>-a</string><string>` + xmlEscape(app) + `</string>
    <string>--args</string><string>-background</string>
  </array>
  <key>RunAtLoad</key><true/>
</dict></plist>
`
	return os.WriteFile(plist, []byte(content), 0o644)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
