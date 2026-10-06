package main

import (
	"encoding/json"
	"runtime"
	"sync"
)

// The native window (Cocoa on macOS, WebView2 on Windows) must own the main
// OS thread.
func init() { runtime.LockOSThread() }

// handleNativeMessage receives JSON messages the page posts to the native shell.
func handleNativeMessage(s string) {
	var m struct {
		Type  string `json:"type"`
		Title string `json:"title"`
		Body  string `json:"body"`
		Chat  string `json:"chat"`
		Count int    `json:"count"`
		Mode  string `json:"mode"`
		Dark  bool   `json:"dark"`
	}
	if json.Unmarshal([]byte(s), &m) != nil {
		return
	}
	switch m.Type {
	case "notify":
		go uiNotify(m.Title, m.Body, m.Chat)
	case "badge":
		go uiBadge(m.Count)
	case "theme":
		go uiTheme(m.Mode, m.Dark)
	case "show":
		go uiShow()
	}
}

var shutdownOnce sync.Once

// shutdown saves state and tells peers we're leaving. Safe to call twice.
func shutdown() {
	shutdownOnce.Do(func() {
		sendBye()
		store.flush()
		peers.save()
		removeInstanceFile()
		installOnQuit()
	})
}
