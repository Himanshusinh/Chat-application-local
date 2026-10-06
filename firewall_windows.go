package main

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unicode/utf16"
)

const canFixFirewall = true

// fixFirewall adds inbound allow rules for this exe (all profiles, so it also
// works when Windows has classified the office network as "Public"). It shows
// the normal UAC prompt because changing firewall rules needs admin rights.
func fixFirewall() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`netsh advfirewall firewall delete rule name="%[1]s" | Out-Null; `+
		`netsh advfirewall firewall add rule name="%[1]s" dir=in action=allow program="%[2]s" enable=yes profile=any | Out-Null; `+
		`netsh advfirewall firewall add rule name="%[1]s" dir=in action=allow protocol=UDP localport=%[3]d profile=any | Out-Null; `+
		`netsh advfirewall firewall add rule name="%[1]s" dir=in action=allow protocol=TCP localport=%[4]d-%[5]d profile=any | Out-Null`,
		appName, exe, discoveryPort, defaultP2PPort, defaultP2PPort+19)
	// -EncodedCommand (UTF-16LE base64) sidesteps all quoting problems with
	// paths like "C:\Program Files\...".
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:], c)
	}
	enc := base64.StdEncoding.EncodeToString(b)
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		fmt.Sprintf(`Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList '-NoProfile -EncodedCommand %s'`, enc))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}
