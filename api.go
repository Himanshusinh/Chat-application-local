package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --------------------------------------------------------------- event hub

type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

var hub = &Hub{clients: map[chan []byte]struct{}{}}

func (h *Hub) broadcast(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	for c := range h.clients {
		select {
		case c <- b:
		default: // slow client; drop rather than block the app
		}
	}
	h.mu.Unlock()
}

func (h *Hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// ------------------------------------------------------------- UI server

func serveUI(ln net.Listener) {
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	mux := http.NewServeMux()
	mux.Handle("/", files)
	api := map[string]http.HandlerFunc{
		"/api/ping":        func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]bool{"ok": true}) },
		"/api/state":       apiState,
		"/api/messages":    apiMessages,
		"/api/send":        apiSend,
		"/api/typing":      apiTyping,
		"/api/read":        apiRead,
		"/api/clear":       apiClear,
		"/api/send-start":  apiSendStart,
		"/api/upload":      apiUpload,
		"/api/send-finish": apiSendFinish,
		"/api/open":        apiOpen,
		"/api/file":        apiFile,
		"/api/settings":    apiSettings,
		"/api/add-peer":    apiAddPeer,
		"/api/remove-peer": apiRemovePeer,
		"/api/open-downloads": func(w http.ResponseWriter, r *http.Request) {
			dl := getCfg().DownloadDir
			_ = os.MkdirAll(dl, 0o755)
			openPath(dl, false)
			writeJSON(w, map[string]bool{"ok": true})
		},
		"/api/quit": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]bool{"ok": true})
			go func() { time.Sleep(200 * time.Millisecond); requestQuit() }()
		},
		"/api/events": apiEvents,
		"/api/show": func(w http.ResponseWriter, r *http.Request) {
			go uiShow()
			writeJSON(w, map[string]bool{"ok": true})
		},
		"/api/update": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, updateState()) },
		"/api/update/check": func(w http.ResponseWriter, r *http.Request) {
			if !updatesEnabled() {
				apiError(w, 400, errors.New("this is a development build; updates are off"))
				return
			}
			go checkForUpdate()
			writeJSON(w, updateState())
		},
		"/api/update/install": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]bool{"ok": true})
			go func() {
				time.Sleep(300 * time.Millisecond)
				if err := installUpdate(true, false); err != nil {
					logger.Printf("install: %v", err)
				}
			}()
		},
		"/api/stickers":        apiStickers,
		"/api/stickers/add":    apiStickerAdd,
		"/api/stickers/save":   apiStickerSave,
		"/api/stickers/remove": apiStickerRemove,
		"/api/sticker":         apiStickerFile,
		"/api/fix-firewall": func(w http.ResponseWriter, r *http.Request) {
			if err := fixFirewall(); err != nil {
				apiError(w, 500, fmt.Errorf("could not change the firewall: %v", err))
				return
			}
			writeJSON(w, map[string]bool{"ok": true})
		},
	}
	for path, h := range api {
		h := h
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			// Only our own window (which knows the token) may use the API;
			// stops other web pages in the browser from poking localhost.
			tok := r.Header.Get("X-Token")
			if tok == "" {
				tok = r.URL.Query().Get("t")
			}
			if tok != uiToken {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			h(w, r)
		})
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil {
		logger.Printf("ui server: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v)
}

func apiError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func apiEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch := make(chan []byte, 512)
	hub.mu.Lock()
	hub.clients[ch] = struct{}{}
	hub.mu.Unlock()
	defer func() {
		hub.mu.Lock()
		delete(hub.clients, ch)
		hub.mu.Unlock()
	}()
	fmt.Fprint(w, ": hi\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func apiState(w http.ResponseWriter, r *http.Request) {
	c := getCfg()
	writeJSON(w, map[string]any{
		"me":             myInfo(),
		"peers":          peers.list(),
		"chats":          store.summaries(),
		"settings":       c,
		"ips":            localIPs(),
		"port":           p2pPort,
		"discovery":      udpConn != nil,
		"canFixFirewall": canFixFirewall,
		"native":         nativeUI,
		"autoStart":      autoStartOn(),
		"os":             runtime.GOOS,
		"version":        version,
		"update":         updateState(),
	})
}

func apiMessages(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > maxPerChat {
		limit = 300
	}
	writeJSON(w, store.messages(r.URL.Query().Get("chat"), limit))
}

func apiSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To      string      `json:"to"`
		Text    string      `json:"text"`
		Sticker *StickerRef `json:"sticker"`
	}
	if err := readJSON(r, &req); err != nil || (strings.TrimSpace(req.Text) == "" && req.Sticker == nil) || req.To == "" {
		apiError(w, 400, errors.New("empty message"))
		return
	}
	m, err := sendMessage(req.To, req.Text, req.Sticker)
	if err != nil {
		apiError(w, 400, err)
		return
	}
	writeJSON(w, m)
}

func apiTyping(w http.ResponseWriter, r *http.Request) {
	var req struct{ To string }
	if readJSON(r, &req) == nil && req.To != "" {
		sendTyping(req.To)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func apiRead(w http.ResponseWriter, r *http.Request) {
	var req struct{ Chat string }
	if readJSON(r, &req) == nil && req.Chat != "" {
		store.markRead(req.Chat)
		if req.Chat != "all" {
			sendReadReceipt(req.Chat)
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func apiClear(w http.ResponseWriter, r *http.Request) {
	var req struct{ Chat string }
	if readJSON(r, &req) == nil && req.Chat != "" {
		store.clear(req.Chat)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// apiSendStart announces a file/folder transfer to the recipients. The
// window then streams the bytes chunk by chunk through apiUpload.
func apiSendStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To     string      `json:"to"`
		Name   string      `json:"name"`
		Folder bool        `json:"folder"`
		Files  []FileEntry `json:"files"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Files) == 0 {
		apiError(w, 400, errors.New("nothing to send"))
		return
	}
	var targets []Peer
	if req.To == "all" {
		targets = peers.online()
		if len(targets) == 0 {
			apiError(w, 409, errors.New("nobody is online to receive it"))
			return
		}
	} else {
		p, ok := peers.get(req.To)
		if !ok || !p.Online {
			apiError(w, 409, errors.New("this person is offline; files can only be sent while they're online"))
			return
		}
		targets = []Peer{p}
	}
	var total int64
	for _, f := range req.Files {
		total += f.Size
	}
	shown := req.Files
	if len(shown) > 50 {
		shown = shown[:50]
	}
	c := getCfg()
	m := &Message{
		ID: newID(), Chat: req.To, From: c.ID, FromName: c.Name, Avatar: c.Avatar, To: req.To, Time: nowMs(), Kind: "files",
		Transfer: &TransferInfo{ID: newID(), Name: req.Name, Count: len(req.Files), Total: total, Folder: req.Folder, Files: shown},
	}
	// Announce to every recipient in parallel; keep the ones that accepted.
	var mu sync.Mutex
	var wg sync.WaitGroup
	var accepted []string
	var lastErr error
	for _, p := range targets {
		wg.Add(1)
		go func(p Peer) {
			defer wg.Done()
			err := postJSON(p.Addr, "/p2p/msg", m)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				lastErr = fmt.Errorf("%s: %v", p.Name, err)
				logger.Printf("announce transfer to %s: %v", p.Name, err)
				return
			}
			accepted = append(accepted, p.ID)
		}(p)
	}
	wg.Wait()
	if len(accepted) == 0 {
		apiError(w, 502, fmt.Errorf("could not reach the recipient (%v)", lastErr))
		return
	}
	m.Transfer.Status = "sending"
	store.add(m, false)
	startTx(m, accepted)
	hub.broadcast(map[string]any{"type": "message", "msg": cloneMsg(m)})
	writeJSON(w, map[string]any{"tid": m.Transfer.ID, "peers": accepted, "msg": m})
}

// apiUpload streams one chunk from the window straight to one peer.
func apiUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t := getTransfer(q.Get("tid"))
	if t == nil || !t.outgoing {
		apiError(w, 404, errors.New("unknown transfer"))
		return
	}
	p, ok := peers.get(q.Get("peer"))
	if !ok {
		apiError(w, 404, errors.New("unknown peer"))
		return
	}
	target := fmt.Sprintf("http://%s/p2p/chunk?tid=%s&rel=%s&size=%s&off=%s",
		p.Addr, url.QueryEscape(t.id), url.QueryEscape(q.Get("rel")), url.QueryEscape(q.Get("size")), url.QueryEscape(q.Get("off")))
	req, _ := http.NewRequestWithContext(r.Context(), "POST", target, countingReader{r.Body, &t.done})
	req.ContentLength = r.ContentLength
	req.Header.Set("Content-Type", "application/octet-stream")
	setP2PHeaders(req)
	resp, err := dataClient.Do(req)
	if err != nil {
		apiError(w, 502, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		apiError(w, resp.StatusCode, httpErr(resp))
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func apiSendFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TID         string   `json:"tid"`
		Status      string   `json:"status"` // done, failed, cancelled
		Error       string   `json:"error"`
		FailedPeers []string `json:"failedPeers"`
	}
	if err := readJSON(r, &req); err != nil {
		apiError(w, 400, err)
		return
	}
	t := getTransfer(req.TID)
	if t == nil {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	notify := req.FailedPeers
	if req.Status != "done" {
		notify = t.peers
	}
	for _, id := range notify {
		if p, ok := peers.get(id); ok {
			st := req.Status
			if st == "done" {
				st = "failed"
			}
			go postJSON(p.Addr, "/p2p/status", statusMsg{TID: t.id, Status: st, Error: req.Error})
		}
	}
	t.finish(req.Status, req.Error)
	writeJSON(w, map[string]bool{"ok": true})
}

func savedPathFor(r *http.Request) (string, error) {
	tid := r.URL.Query().Get("tid")
	if tid == "" {
		var req struct{ TID string }
		_ = readJSON(r, &req)
		tid = req.TID
	}
	m := store.findTransfer(tid)
	if m == nil || m.Transfer.SavedPath == "" {
		return "", errors.New("file not found")
	}
	if _, err := os.Stat(m.Transfer.SavedPath); err != nil {
		return "", errors.New("the file was moved or deleted")
	}
	return m.Transfer.SavedPath, nil
}

func apiOpen(w http.ResponseWriter, r *http.Request) {
	reveal := r.URL.Query().Get("reveal") == "1"
	p, err := savedPathFor(r)
	if err != nil {
		apiError(w, 404, err)
		return
	}
	openPath(p, reveal)
	writeJSON(w, map[string]bool{"ok": true})
}

// apiFile serves a received file to the window (image/video previews).
func apiFile(w http.ResponseWriter, r *http.Request) {
	p, err := savedPathFor(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		http.Error(w, "not a file", 404)
		return
	}
	http.ServeFile(w, r, p)
}

func apiSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            *string `json:"name"`
		Avatar          *string `json:"avatar"`
		TeamKey         *string `json:"teamKey"`
		DownloadDir     *string `json:"downloadDir"`
		ReopenOnMessage *bool   `json:"reopenOnMessage"`
		AutoStart       *bool   `json:"autoStart"`
		Theme           *string `json:"theme"`
	}
	if err := readJSON(r, &req); err != nil {
		apiError(w, 400, err)
		return
	}
	if req.DownloadDir != nil {
		d := strings.TrimSpace(*req.DownloadDir)
		if d == "" {
			d = defaultDownloadDir()
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			apiError(w, 400, fmt.Errorf("can't use that folder: %v", err))
			return
		}
		req.DownloadDir = &d
	}
	updateCfg(func(c *Config) {
		if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
			c.Name = strings.TrimSpace(*req.Name)
		}
		if req.Avatar != nil && *req.Avatar != "" {
			c.Avatar = *req.Avatar
		}
		if req.TeamKey != nil && strings.TrimSpace(*req.TeamKey) != "" {
			c.TeamKey = strings.TrimSpace(*req.TeamKey)
		}
		if req.DownloadDir != nil {
			c.DownloadDir = *req.DownloadDir
		}
		if req.ReopenOnMessage != nil {
			c.ReopenOnMessage = *req.ReopenOnMessage
		}
		if req.AutoStart != nil {
			v := *req.AutoStart
			c.AutoStart = &v
		}
		if req.Theme != nil && (*req.Theme == "light" || *req.Theme == "dark" || *req.Theme == "system") {
			c.Theme = *req.Theme
		}
	})
	if req.AutoStart != nil {
		applyAutoStart()
	}
	announceNow() // name/picture changes show up for everyone immediately
	apiState(w, r)
}

func apiAddPeer(w http.ResponseWriter, r *http.Request) {
	var req struct{ Addr string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Addr) == "" {
		apiError(w, 400, errors.New("enter an IP address or host name"))
		return
	}
	a := strings.TrimSpace(req.Addr)
	a = strings.TrimPrefix(strings.TrimPrefix(a, "http://"), "https://")
	a = strings.TrimSuffix(a, "/")
	addr := normalizeAddr(a)
	info, err := hello(addr)
	if err != nil {
		apiError(w, 502, fmt.Errorf("could not reach %s: %v. Check that OfficeChat is running there, the address is right, the firewall allows it, and you use the same team key", addr, err))
		return
	}
	updateCfg(func(c *Config) {
		for _, x := range c.ManualPeers {
			if x == addr {
				return
			}
		}
		c.ManualPeers = append(c.ManualPeers, addr)
	})
	writeJSON(w, info)
}

func apiRemovePeer(w http.ResponseWriter, r *http.Request) {
	var req struct{ Addr string }
	_ = readJSON(r, &req)
	updateCfg(func(c *Config) {
		out := c.ManualPeers[:0]
		for _, x := range c.ManualPeers {
			if x != req.Addr {
				out = append(out, x)
			}
		}
		c.ManualPeers = out
	})
	apiState(w, r)
}
