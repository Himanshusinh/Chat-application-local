package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// Two HTTP clients: a short-timeout one for chat/control traffic and an
// untimed one with big buffers for file data. Proxy is nil on purpose: office
// PCs often have a system proxy configured that must not see LAN traffic.
var (
	ctlClient = &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 4 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     60 * time.Second,
		},
	}
	dataClient = &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 6 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
			WriteBufferSize:     512 << 10,
			ReadBufferSize:      512 << 10,
		},
	}
)

func setP2PHeaders(req *http.Request) {
	req.Header.Set("X-OC-Team", teamHash())
	req.Header.Set("X-OC-From", getCfg().ID)
}

func httpErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("peer replied %d: %s", resp.StatusCode, bytes.TrimSpace(b))
}

func postJSON(addr, path string, v any) error {
	body, _ := json.Marshal(v)
	req, _ := http.NewRequest("POST", "http://"+addr+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	setP2PHeaders(req)
	resp, err := ctlClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return httpErr(resp)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func serveP2P(ln net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("/p2p/hello", p2pHello)
	mux.HandleFunc("/p2p/msg", p2pMsg)
	mux.HandleFunc("/p2p/typing", p2pTyping)
	mux.HandleFunc("/p2p/read", p2pRead)
	mux.HandleFunc("/p2p/chunk", p2pChunk)
	mux.HandleFunc("/p2p/status", p2pStatus)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-OC-Team") != teamHash() {
				http.Error(w, "wrong team key", http.StatusForbidden)
				return
			}
			mux.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if err := srv.Serve(ln); err != nil {
		logger.Printf("p2p server: %v", err)
	}
}

func remoteIP(r *http.Request) string {
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	return h
}

// touch marks the caller online; any authenticated request proves it's alive.
func touch(r *http.Request) {
	id := r.Header.Get("X-OC-From")
	if p, ok := peers.get(id); ok {
		info := p.PeerInfo
		via := "direct"
		if time.Since(p.lastLAN) < 7*time.Second {
			via = "lan"
		}
		peers.seen(info, remoteIP(r), via)
	}
}

func p2pHello(w http.ResponseWriter, r *http.Request) {
	var info PeerInfo
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&info); err != nil {
		http.Error(w, "bad hello", 400)
		return
	}
	via := "direct"
	if p, ok := peers.get(info.ID); ok && time.Since(p.lastLAN) < 7*time.Second {
		via = "lan"
	}
	peers.seen(info, remoteIP(r), via)
	var known []Peer
	for _, p := range peers.online() {
		if p.ID != info.ID {
			known = append(known, p)
		}
	}
	writeJSON(w, helloResp{Me: myInfo(), Peers: known})
}

func p2pMsg(w http.ResponseWriter, r *http.Request) {
	var m Message
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&m); err != nil || m.ID == "" {
		http.Error(w, "bad message", 400)
		return
	}
	me := getCfg().ID
	if m.From != r.Header.Get("X-OC-From") || (m.To != "all" && m.To != me) {
		http.Error(w, "not for me", 400)
		return
	}
	touch(r)
	m.Chat = m.From
	if m.To == "all" {
		m.Chat = "all"
	}
	m.Status = ""
	if m.Kind == "sticker" {
		if err := acceptSticker(m.Sticker); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	if m.Kind == "files" {
		if m.Transfer == nil || m.Transfer.ID == "" {
			http.Error(w, "bad transfer", 400)
			return
		}
		m.Transfer.Status = "receiving"
		m.Transfer.Done = 0
		m.Transfer.SavedPath = ""
	}
	if store.add(&m, true) {
		if m.Kind == "files" {
			startRx(&m)
		}
		hub.broadcast(map[string]any{"type": "message", "msg": cloneMsg(&m)})
		maybeReopenWindow()
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type typingMsg struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func p2pTyping(w http.ResponseWriter, r *http.Request) {
	var t typingMsg
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&t) == nil {
		chat := t.From
		if t.To == "all" {
			chat = "all"
		}
		hub.broadcast(map[string]any{"type": "typing", "chat": chat, "from": t.From})
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func p2pRead(w http.ResponseWriter, r *http.Request) {
	from := r.Header.Get("X-OC-From")
	if store.markOutgoingRead(from) {
		hub.broadcast(map[string]any{"type": "reload", "chat": from})
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func p2pChunk(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tr := getTransfer(q.Get("tid"))
	if tr == nil || tr.outgoing || tr.from != r.Header.Get("X-OC-From") {
		http.Error(w, "unknown transfer", 404)
		return
	}
	size, err1 := strconv.ParseInt(q.Get("size"), 10, 64)
	off, err2 := strconv.ParseInt(q.Get("off"), 10, 64)
	if err1 != nil || err2 != nil || size < 0 || off < 0 || off > size {
		http.Error(w, "bad range", 400)
		return
	}
	if err := tr.receiveChunk(q.Get("rel"), size, off, r.ContentLength, r.Body); err != nil {
		if errors.Is(err, errFinished) {
			http.Error(w, err.Error(), 410)
			return
		}
		logger.Printf("chunk %s %s: %v", tr.id, q.Get("rel"), err)
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type statusMsg struct {
	TID    string `json:"tid"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func p2pStatus(w http.ResponseWriter, r *http.Request) {
	var s statusMsg
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&s) == nil {
		if tr := getTransfer(s.TID); tr != nil && !tr.outgoing && tr.from == r.Header.Get("X-OC-From") {
			if s.Status == "failed" || s.Status == "cancelled" {
				tr.finish(s.Status, s.Error)
			}
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ------------------------------------------------------------ outgoing chat

func sendMessage(to, text string, st *StickerRef) (*Message, error) {
	c := getCfg()
	m := &Message{ID: newID(), Chat: to, From: c.ID, FromName: c.Name, Avatar: c.Avatar, To: to, Text: text, Time: nowMs(), Kind: "text"}
	if st != nil {
		if !st.valid() {
			return nil, errors.New("unknown sticker")
		}
		if st.custom() {
			if _, err := os.Stat(st.path()); err != nil {
				return nil, errors.New("sticker image is missing")
			}
		}
		m.Kind, m.Text, m.Sticker = "sticker", "", &StickerRef{ID: st.ID, Ext: st.Ext}
	}
	if to == "all" {
		m.Status = "sent"
		store.add(m, false)
		wire := wireMsg(m)
		for _, p := range peers.online() {
			go func(p Peer) {
				if err := postJSON(p.Addr, "/p2p/msg", wire); err != nil {
					logger.Printf("send to %s: %v", p.Name, err)
				}
			}(p)
		}
		return m, nil
	}
	if _, ok := peers.get(to); !ok {
		return nil, errors.New("unknown person")
	}
	m.Status = "pending"
	store.add(m, false)
	go flushPending(to)
	return m, nil
}

var flushing sync.Map // peer ID -> struct{}

// flushPending delivers queued direct messages, e.g. when someone comes back online.
func flushPending(peerID string) {
	if _, busy := flushing.LoadOrStore(peerID, struct{}{}); busy {
		return
	}
	defer flushing.Delete(peerID)
	p, ok := peers.get(peerID)
	if !ok || !p.Online {
		return
	}
	for _, m := range store.pending(peerID) {
		if err := postJSON(p.Addr, "/p2p/msg", wireMsg(m)); err != nil {
			logger.Printf("deliver to %s: %v", p.Name, err)
			return
		}
		if u := store.update(peerID, m.ID, func(x *Message) { x.Status = "sent" }); u != nil {
			hub.broadcast(map[string]any{"type": "update", "msg": u})
		}
	}
}

func sendTyping(to string) {
	t := typingMsg{From: getCfg().ID, To: to}
	var targets []Peer
	if to == "all" {
		targets = peers.online()
	} else if p, ok := peers.get(to); ok && p.Online {
		targets = []Peer{p}
	}
	for _, p := range targets {
		go postJSON(p.Addr, "/p2p/typing", t)
	}
}

func sendReadReceipt(peerID string) {
	if p, ok := peers.get(peerID); ok && p.Online {
		go postJSON(p.Addr, "/p2p/read", map[string]string{})
	}
}

var (
	reopenMu   sync.Mutex
	lastReopen time.Time
)

// maybeReopenWindow brings the window back when a message arrives while it's
// hidden, if the user switched that on in Settings.
func maybeReopenWindow() {
	if !getCfg().ReopenOnMessage || uiVisible() {
		return
	}
	reopenMu.Lock()
	defer reopenMu.Unlock()
	if time.Since(lastReopen) < time.Minute {
		return
	}
	lastReopen = time.Now()
	go uiShow()
}
