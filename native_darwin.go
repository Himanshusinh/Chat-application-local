//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit -framework UserNotifications
#include <stdlib.h>
void ocRun(const char *url, int hidden);
void ocShow(void);
void ocQuit(void);
void ocEval(const char *js);
void ocBadge(const char *label);
void ocNotify(const char *title, const char *body, const char *chat);
int ocVisible(void);
void ocTheme(int mode);
*/
import "C"

import (
	"strconv"
	"unsafe"
)

const nativeUI = true

func cstr(s string) (*C.char, func()) {
	c := C.CString(s)
	return c, func() { C.free(unsafe.Pointer(c)) }
}

// runUI runs the Cocoa event loop on the main thread. It only returns via
// process exit (Cmd+Q / Quit), after goWillQuit has saved everything.
func runUI(url string, hidden bool) {
	c, free := cstr(url)
	defer free()
	h := 0
	if hidden {
		h = 1
	}
	C.ocRun(c, C.int(h))
}

func uiShow()         { C.ocShow() }
func uiQuit()         { C.ocQuit() }
func uiVisible() bool { return C.ocVisible() != 0 }

func uiEval(js string) {
	c, free := cstr(js)
	defer free()
	C.ocEval(c)
}

func uiBadge(n int) {
	label := ""
	if n > 0 {
		label = strconv.Itoa(n)
	}
	c, free := cstr(label)
	defer free()
	C.ocBadge(c)
}

func uiNotify(title, body, chat string) {
	t, f1 := cstr(title)
	b, f2 := cstr(body)
	ch, f3 := cstr(chat)
	defer f1()
	defer f2()
	defer f3()
	C.ocNotify(t, b, ch)
}

// uiTheme matches the window frame to the page: 0 = follow system, 1 = light, 2 = dark.
func uiTheme(mode string, dark bool) {
	m := 0
	if mode != "system" {
		m = 1
		if dark {
			m = 2
		}
	}
	C.ocTheme(C.int(m))
}

//export goNativeMessage
func goNativeMessage(s *C.char) { handleNativeMessage(C.GoString(s)) }

//export goWillQuit
func goWillQuit() { shutdown() }
