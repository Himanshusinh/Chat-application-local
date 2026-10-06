//go:build !windows && !(darwin && cgo)

package main

// Fallback when no native window is available (Linux, or a Mac build
// without cgo): the UI opens in a Chromium app window or the browser.

const nativeUI = false

var uiDone = make(chan struct{})

func runUI(url string, hidden bool) {
	if !hidden {
		openAppWindow(url)
	}
	<-uiDone
}

func uiShow()                           { openAppWindow(uiURL()) }
func uiQuit()                           { defer func() { recover() }(); close(uiDone) }
func uiVisible() bool                   { return hub.count() > 0 }
func uiEval(string)                     {}
func uiBadge(int)                       {}
func uiNotify(title, body, chat string) {}
func uiTheme(mode string, dark bool)    {}
