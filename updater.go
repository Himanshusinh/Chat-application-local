package main

// Automatic updates.
//
// Every push to main makes GitHub Actions build a new release and publish
// latest.json (+ an ed25519 signature) next to the files. Each OfficeChat
// checks it at start-up and every few hours. A newer version is downloaded
// in the background, verified (signature + SHA-256), and then:
//   - installed and restarted right away if the window is hidden and no
//     files are moving, or
//   - offered with a "Restart to update" button, and installed on quit.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updateRepo     = "Himanshusinh/Chat-application-local"
	updateManifest = "https://github.com/" + updateRepo + "/releases/latest/download/latest.json"
	// Public half of the release signing key. Only builds signed with the
	// matching private key (a GitHub secret) are ever installed.
	updatePublicKey = "ZmBboAe6Ut5shjiHe7+0QfuQAGLS7oAA28tBxERQLDc="
	updateEvery     = 4 * time.Hour
)

type updAsset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type updManifest struct {
	Version string              `json:"version"`
	Notes   string              `json:"notes"`
	Date    string              `json:"date"`
	Assets  map[string]updAsset `json:"assets"`
}

type UpdateState struct {
	Current string `json:"current"`
	State   string `json:"state"` // idle, checking, uptodate, downloading, ready, installing, error, disabled
	Version string `json:"version,omitempty"`
	Notes   string `json:"notes,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    int64  `json:"done,omitempty"`
	Total   int64  `json:"total,omitempty"`
}

var (
	updMu      sync.Mutex
	upd        = UpdateState{State: "idle"}
	updStaged  string // verified file ready to install
	updBusy    bool
	updApplied bool
)

// The internet download uses the system proxy (unlike LAN traffic).
var updClient = &http.Client{Timeout: 30 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}

func updateState() UpdateState {
	updMu.Lock()
	defer updMu.Unlock()
	s := upd
	s.Current = version
	return s
}

func setUpdate(fn func(s *UpdateState)) {
	updMu.Lock()
	fn(&upd)
	s := upd
	s.Current = version
	updMu.Unlock()
	hub.broadcast(map[string]any{"type": "appupdate", "update": s})
}

func updatesEnabled() bool { return version != "dev" }

func runUpdater() {
	cleanupOldVersion()
	if !updatesEnabled() {
		setUpdate(func(s *UpdateState) { s.State = "disabled" })
		return
	}
	time.Sleep(20 * time.Second) // let the app settle first
	for {
		checkForUpdate()
		time.Sleep(updateEvery)
	}
}

// checkForUpdate fetches the signed manifest and, if there is a newer
// version, downloads it in the background.
func checkForUpdate() {
	updMu.Lock()
	if updBusy || updStaged != "" {
		updMu.Unlock()
		return
	}
	updBusy = true
	updMu.Unlock()
	defer func() { updMu.Lock(); updBusy = false; updMu.Unlock() }()

	setUpdate(func(s *UpdateState) { s.State = "checking"; s.Error = "" })
	m, err := fetchManifest()
	if err != nil {
		logger.Printf("update check: %v", err)
		setUpdate(func(s *UpdateState) { s.State = "error"; s.Error = err.Error() })
		return
	}
	if !newerVersion(m.Version, version) {
		setUpdate(func(s *UpdateState) { s.State = "uptodate"; s.Version = m.Version })
		return
	}
	a, ok := m.Assets[platformKey()]
	if !ok {
		setUpdate(func(s *UpdateState) { s.State = "error"; s.Error = "no update for this computer type" })
		return
	}
	logger.Printf("update: %s -> %s, downloading", version, m.Version)
	setUpdate(func(s *UpdateState) {
		s.State, s.Version, s.Notes, s.Done, s.Total = "downloading", m.Version, m.Notes, 0, a.Size
	})
	path, err := downloadAsset(a)
	if err != nil {
		logger.Printf("update download: %v", err)
		setUpdate(func(s *UpdateState) { s.State = "error"; s.Error = "download failed: " + err.Error() })
		return
	}
	updMu.Lock()
	updStaged = path
	updMu.Unlock()
	setUpdate(func(s *UpdateState) { s.State = "ready" })
	logger.Printf("update %s ready", m.Version)
	maybeAutoInstall()
}

// manifestURL can be pointed at a local server for testing.
func manifestURL() string {
	if u := os.Getenv("OFFICECHAT_UPDATE_URL"); u != "" {
		return u
	}
	return updateManifest
}

func fetchManifest() (*updManifest, error) {
	body, err := httpGet(manifestURL(), 1<<20)
	if err != nil {
		return nil, err
	}
	sig, err := httpGet(manifestURL()+".sig", 4096)
	if err != nil {
		return nil, err
	}
	pub, _ := base64.StdEncoding.DecodeString(updatePublicKey)
	rawSig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), body, rawSig) {
		return nil, errors.New("update signature is not valid; ignoring it")
	}
	var m updManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func httpGet(url string, limit int64) ([]byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", appName+"/"+version)
	resp, err := updClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach the update server (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, errors.New("no release published yet")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("update server replied %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

type progressWriter struct{ n int64 }

func (p *progressWriter) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	return len(b), nil
}

func downloadAsset(a updAsset) (string, error) {
	dir := filepath.Join(dataDir, "update")
	_ = os.MkdirAll(dir, 0o755)
	dest := filepath.Join(dir, filepath.Base(a.URL))
	req, _ := http.NewRequest("GET", a.URL, nil)
	req.Header.Set("User-Agent", appName+"/"+version)
	resp, err := updClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("server replied %s", resp.Status)
	}
	f, err := os.Create(dest + ".part")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	pw := &progressWriter{}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(500 * time.Millisecond):
				n := pw.n
				setUpdate(func(s *UpdateState) { s.Done = n })
			}
		}
	}()
	_, err = io.Copy(io.MultiWriter(f, h, pw), resp.Body)
	close(stop)
	f.Close()
	if err != nil {
		os.Remove(dest + ".part")
		return "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(a.SHA256) {
		os.Remove(dest + ".part")
		return "", errors.New("downloaded file is corrupted (checksum mismatch)")
	}
	if err := os.Rename(dest+".part", dest); err != nil {
		return "", err
	}
	return dest, nil
}

func platformKey() string {
	if runtime.GOOS == "darwin" {
		return "darwin-universal"
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}

// newerVersion reports whether a > b for dotted numeric versions.
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func transfersActive() bool {
	transfersMu.Lock()
	defer transfersMu.Unlock()
	for _, t := range transfers {
		t.mu.Lock()
		done := t.finished
		t.mu.Unlock()
		if !done {
			return true
		}
	}
	return false
}

// maybeAutoInstall installs right away when nobody is looking at the app
// and no files are moving; it restarts hidden, so the user never notices.
func maybeAutoInstall() {
	if uiVisible() || transfersActive() {
		return
	}
	if err := installUpdate(true, true); err != nil {
		logger.Printf("auto-install: %v", err)
	}
}

// installUpdate replaces the app with the staged version. With restart, the
// new version is started (hidden if background) and this one quits.
func installUpdate(restart, background bool) error {
	updMu.Lock()
	path := updStaged
	if path == "" || updApplied {
		updMu.Unlock()
		return errors.New("no update ready")
	}
	updApplied = true
	updMu.Unlock()
	setUpdate(func(s *UpdateState) { s.State = "installing" })
	relaunch, err := applyUpdateFile(path)
	if err != nil {
		updMu.Lock()
		updApplied = false
		updMu.Unlock()
		setUpdate(func(s *UpdateState) { s.State = "error"; s.Error = "install failed: " + err.Error() })
		return err
	}
	logger.Printf("update installed (%s)", updateState().Version)
	_ = os.Remove(path)
	if restart {
		shutdown() // save everything and free the ports before the new copy starts
		if err := relaunch(background); err != nil {
			logger.Printf("relaunch: %v", err)
		}
		requestQuit()
	}
	return nil
}

// installOnQuit is called from shutdown: a downloaded update is put in
// place so the next start is already the new version.
func installOnQuit() {
	updMu.Lock()
	ready := updStaged != "" && !updApplied
	updMu.Unlock()
	if ready {
		if err := installUpdate(false, false); err != nil {
			logger.Printf("install on quit: %v", err)
		}
	}
}
