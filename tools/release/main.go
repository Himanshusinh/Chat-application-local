// release: signing tool for OfficeChat auto-updates.
//
//	go run ./tools/release keygen
//	    Prints a new private key (keep secret: GitHub secret UPDATE_SIGNING_KEY)
//	    and the public key (goes in updater.go).
//	UPDATE_SIGNING_KEY=... go run ./tools/release manifest -version 1.2.3 -dir out -base <download url> -notes "..."
//	    Writes out/latest.json and out/latest.json.sig for the files in out/.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Update files per platform (names must match the build scripts and updater.go).
var platformFiles = map[string]string{
	"windows-amd64":    "OfficeChat-windows-amd64.exe",
	"windows-arm64":    "OfficeChat-windows-arm64.exe",
	"darwin-universal": "OfficeChat-mac.zip",
}

type asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type manifest struct {
	Version string           `json:"version"`
	Notes   string           `json:"notes"`
	Date    string           `json:"date"`
	Assets  map[string]asset `json:"assets"`
}

func main() {
	if len(os.Args) < 2 {
		fail("usage: release keygen | release manifest -version V -dir DIR -base URL [-notes TEXT]")
	}
	switch os.Args[1] {
	case "keygen":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		check(err)
		fmt.Println("PRIVATE (secret UPDATE_SIGNING_KEY):", base64.StdEncoding.EncodeToString(priv.Seed()))
		fmt.Println("PUBLIC  (updater.go updatePublicKey):", base64.StdEncoding.EncodeToString(pub))
	case "manifest":
		fs := flag.NewFlagSet("manifest", flag.ExitOnError)
		ver := fs.String("version", "", "release version, e.g. 1.2.3")
		dir := fs.String("dir", "out", "folder with the built files")
		base := fs.String("base", "", "download URL prefix for the files")
		notes := fs.String("notes", "", "release notes")
		_ = fs.Parse(os.Args[2:])
		if *ver == "" || *base == "" {
			fail("-version and -base are required")
		}
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("UPDATE_SIGNING_KEY")))
		if err != nil || len(seed) != ed25519.SeedSize {
			fail("UPDATE_SIGNING_KEY is missing or invalid (add it in GitHub › Settings › Secrets › Actions)")
		}
		priv := ed25519.NewKeyFromSeed(seed)
		m := manifest{Version: *ver, Notes: firstLines(*notes, 6), Date: time.Now().UTC().Format(time.RFC3339), Assets: map[string]asset{}}
		for plat, name := range platformFiles {
			path := filepath.Join(*dir, name)
			f, err := os.Open(path)
			check(err)
			h := sha256.New()
			n, err := io.Copy(h, f)
			f.Close()
			check(err)
			m.Assets[plat] = asset{URL: strings.TrimRight(*base, "/") + "/" + name, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
		}
		b, _ := json.MarshalIndent(m, "", "  ")
		check(os.WriteFile(filepath.Join(*dir, "latest.json"), b, 0o644))
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b))
		check(os.WriteFile(filepath.Join(*dir, "latest.json.sig"), []byte(sig), 0o644))
		fmt.Printf("signed manifest for %s (%d platforms)\n", *ver, len(m.Assets))
	default:
		fail("unknown command " + os.Args[1])
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func check(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
