package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func isInstalled() bool {
	exe, _ := os.Executable()
	return strings.Contains(strings.ToLower(exe), `\program files`)
}

// setAutoStart adds/removes OfficeChat in HKCU\...\Run so it starts (hidden,
// in the tray) when the user signs in.
func setAutoStart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(appName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(appName, `"`+exe+`" -background`)
}
