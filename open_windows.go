package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// openAppWindow shows the UI in an Edge (always present on Windows 10/11) or
// Chrome app window with its own profile, so it looks and acts like an app.
func openAppWindow(url string) {
	var candidates []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(base, `BraveSoftware\Brave-Browser\Application\brave.exe`))
	}
	for _, exe := range candidates {
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		cmd := exec.Command(exe, "--app="+url, "--user-data-dir="+filepath.Join(dataDir, "window"),
			"--no-first-run", "--no-default-browser-check", "--window-size=1180,780", "--disable-features=Translate")
		if cmd.Start() == nil {
			return
		}
	}
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func openPath(p string, reveal bool) {
	if reveal {
		// explorer needs exactly: /select,"C:\path with spaces\file"
		cmd := exec.Command("explorer")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer /select,"` + p + `"`}
		_ = cmd.Start()
		return
	}
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", p).Start()
}
