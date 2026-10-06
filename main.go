// OfficeChat: peer-to-peer office chat and file sharing for Windows and macOS.
//
// Every computer runs one copy of this program. It finds the others on the
// LAN by UDP broadcast, talks to them directly over HTTP (no central server),
// and shows its window in a Chromium "app" window (Edge/Chrome) or the
// default browser. No Electron, no external dependencies.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

// version is stamped in by the build: -ldflags "-X main.version=1.2.3".
// "dev" builds never auto-update.
var version = "dev"

const (
	appName        = "OfficeChat"
	discoveryPort  = 45454
	defaultP2PPort = 45456
	defaultUIPort  = 45480
)

type Config struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Avatar          string   `json:"avatar"`
	TeamKey         string   `json:"teamKey"`
	P2PPort         int      `json:"p2pPort"`
	DownloadDir     string   `json:"downloadDir"`
	ManualPeers     []string `json:"manualPeers"`
	ReopenOnMessage bool     `json:"reopenOnMessage"`
	AutoStart       *bool    `json:"autoStart,omitempty"`
	Theme           string   `json:"theme"` // light, dark or system
}

var (
	dataDir  string
	cfg      Config
	cfgMu    sync.RWMutex
	logger   *log.Logger
	p2pPort  int
	uiPort   int
	uiToken  string
	quitCh   = make(chan struct{})
	quitOnce sync.Once
)

func main() {
	noWindow := flag.Bool("nowindow", false, "headless: no window at all (servers/testing)")
	background := flag.Bool("background", false, "start hidden (used when starting at login)")
	waitPID := flag.Int("waitpid", 0, "wait for this process to exit first (used after an update)")
	flag.Parse()
	if *waitPID > 0 {
		waitForExit(*waitPID, 30*time.Second)
	}

	dataDir = appDataDir()
	_ = os.MkdirAll(dataDir, 0o755)
	setupLog()

	// Second launch (double-click again): just bring up the running copy's window.
	if url := runningInstanceURL(); url != "" {
		if !callRunning(url, "api/show") {
			openAppWindow(url)
		}
		return
	}

	loadConfig()
	store = newStore(filepath.Join(dataDir, "history.json"))
	peers = newPeerTable(filepath.Join(dataDir, "peers.json"))

	p2pLn, port, err := listenWithFallback("0.0.0.0", getCfg().P2PPort)
	if err != nil {
		fatal("cannot open network port: %v", err)
	}
	p2pPort = port
	uiLn, port, err := listenWithFallback("127.0.0.1", defaultUIPort)
	if err != nil {
		fatal("cannot open UI port: %v", err)
	}
	uiPort = port
	uiToken = randHex(16)

	go serveP2P(p2pLn)
	go serveUI(uiLn)
	go runDiscovery()
	go runPinger()
	go runPeerWatcher()
	go runProgressTicker()
	go runUpdater()
	go store.persistLoop()

	url := uiURL()
	writeInstanceFile(url)
	logger.Printf("%s %s ready: UI %s, peer port %d, data %s", appName, version, url, p2pPort, dataDir)
	fmt.Printf("%s is running.\n  Window:    %s\n  Peer port: %d\n  Data:      %s\n", appName, url, p2pPort, dataDir)

	applyAutoStart()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	if *noWindow {
		select {
		case <-sig:
		case <-quitCh:
		}
		shutdown()
		return
	}
	go func() {
		select {
		case <-sig:
		case <-quitCh:
		}
		shutdown()
		uiQuit()
	}()
	runUI(url, *background) // blocks on the main thread until quit
	shutdown()
}

func removeInstanceFile() { _ = os.Remove(filepath.Join(dataDir, "instance.json")) }

func requestQuit() { quitOnce.Do(func() { close(quitCh) }) }

func uiURL() string { return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", uiPort, uiToken) }

func appDataDir() string {
	if d := os.Getenv("OFFICECHAT_DATA"); d != "" { // lets you run two copies on one machine for testing
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base, _ = os.UserHomeDir()
	}
	return filepath.Join(base, appName)
}

func setupLog() {
	f, err := os.OpenFile(filepath.Join(dataDir, "officechat.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	var w io.Writer = os.Stderr
	if err == nil {
		if fi, _ := f.Stat(); fi != nil && fi.Size() > 5<<20 {
			_ = f.Truncate(0)
		}
		w = f
	}
	logger = log.New(w, "", log.LstdFlags)
}

func fatal(format string, a ...any) {
	logger.Printf("FATAL: "+format, a...)
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

func defaultDownloadDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads", appName)
}

func loadConfig() {
	path := filepath.Join(dataDir, "config.json")
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg.ID == "" {
		cfg.ID = randHex(8)
	}
	if cfg.Name == "" {
		cfg.Name = defaultName()
	}
	if cfg.Avatar == "" {
		cfg.Avatar = "🙂"
	}
	if cfg.TeamKey == "" {
		cfg.TeamKey = "office"
	}
	if cfg.P2PPort == 0 {
		cfg.P2PPort = defaultP2PPort
	}
	if cfg.AutoStart == nil {
		// Installed copies start at login by default; a copy run from a
		// download folder doesn't register itself.
		on := isInstalled()
		cfg.AutoStart = &on
	}
	if cfg.Theme == "" {
		cfg.Theme = "dark"
	}
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = defaultDownloadDir()
	}
	// Messages, file progress and updates never raise the window. It appears
	// only when the user opens it.
	cfg.ReopenOnMessage = false
	saveConfigLocked()
}

func defaultName() string {
	for _, k := range []string{"USER", "USERNAME"} {
		if v := os.Getenv(k); v != "" {
			h, _ := os.Hostname()
			h = strings.TrimSuffix(h, ".local")
			if h != "" {
				return v + " (" + h + ")"
			}
			return v
		}
	}
	h, _ := os.Hostname()
	return strings.TrimSuffix(h, ".local")
}

func saveConfigLocked() {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	writeFileAtomic(filepath.Join(dataDir, "config.json"), b)
}

func getCfg() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	c := cfg
	c.ManualPeers = append([]string(nil), cfg.ManualPeers...)
	return c
}

func updateCfg(fn func(c *Config)) {
	cfgMu.Lock()
	fn(&cfg)
	saveConfigLocked()
	cfgMu.Unlock()
}

// teamHash is what peers compare to know they belong to the same office.
// The raw key never goes on the wire.
func teamHash() string {
	sum := sha256.Sum256([]byte("officechat:" + getCfg().TeamKey))
	return hex.EncodeToString(sum[:16])
}

func myInfo() PeerInfo {
	c := getCfg()
	return PeerInfo{ID: c.ID, Name: c.Name, Avatar: c.Avatar, OS: runtime.GOOS, Port: p2pPort, Version: version}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newID() string { return fmt.Sprintf("%x-%s", time.Now().UnixMilli(), randHex(6)) }

func nowMs() int64 { return time.Now().UnixMilli() }

func writeFileAtomic(path string, b []byte) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		logger.Printf("write %s: %v", path, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		// Windows can refuse rename over a file another process has open.
		_ = os.Remove(path)
		_ = os.Rename(tmp, path)
	}
}

func listenWithFallback(host string, port int) (net.Listener, int, error) {
	var lastErr error
	for p := port; p < port+20; p++ {
		ln, err := net.Listen("tcp4", net.JoinHostPort(host, fmt.Sprint(p)))
		if err == nil {
			return ln, p, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

type instanceFile struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

func writeInstanceFile(url string) {
	b, _ := json.Marshal(instanceFile{URL: url, PID: os.Getpid()})
	writeFileAtomic(filepath.Join(dataDir, "instance.json"), b)
}

// callRunning asks the already-running copy to do something (e.g. show its window).
func callRunning(url, path string) bool {
	i := strings.Index(url, "?t=")
	if i < 0 {
		return false
	}
	req, _ := http.NewRequest("POST", url[:i]+path, strings.NewReader("{}"))
	req.Header.Set("X-Token", url[i+3:])
	resp, err := (&http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// runningInstanceURL returns the UI URL of an already running copy, if any.
func runningInstanceURL() string {
	b, err := os.ReadFile(filepath.Join(dataDir, "instance.json"))
	if err != nil {
		return ""
	}
	var inst instanceFile
	if json.Unmarshal(b, &inst) != nil || inst.URL == "" {
		return ""
	}
	i := strings.Index(inst.URL, "?t=")
	if i < 0 {
		return ""
	}
	req, _ := http.NewRequest("GET", inst.URL[:i]+"api/ping", nil)
	req.Header.Set("X-Token", inst.URL[i+3:])
	resp, err := (&http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	return inst.URL
}

func autoStartOn() bool {
	c := getCfg()
	return c.AutoStart != nil && *c.AutoStart
}

func applyAutoStart() {
	if err := setAutoStart(autoStartOn()); err != nil {
		logger.Printf("auto-start: %v", err)
	}
}
