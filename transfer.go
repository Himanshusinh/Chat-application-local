package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// File transfer design (fast on LAN, robust over long distance):
//   - Files are split into chunks; several chunks/files move in parallel over
//     separate TCP connections, which fills the pipe on high-latency links.
//   - The receiver writes each chunk straight to its offset in a ".part"
//     file (no temp copies), then renames it when every byte has arrived.
//   - A failed chunk is simply re-sent; writes at an offset are idempotent.
//   - Paths travel as "a/b/c" and are rebuilt with the local separator, with
//     names that Windows can't store (":", "?", "CON", …) made safe.

var errFinished = errors.New("transfer already finished")

const stallTimeout = 3 * time.Minute

type rxFile struct {
	f     *os.File
	part  string
	final string
	size  int64
	got   int64
}

type Transfer struct {
	id       string
	chat     string
	msgID    string
	from     string
	outgoing bool
	total    int64
	count    int
	peers    []string
	done     atomic.Int64

	mu        sync.Mutex
	files     map[string]*rxFile
	completed map[string]bool
	roots     map[string]string
	firstPath string
	filesDone int
	finished  bool
	lastDone  int64
	speed     float64
	active    time.Time // last time bytes moved
}

var (
	transfersMu sync.Mutex
	transfers   = map[string]*Transfer{}
	reserved    = map[string]bool{} // download paths claimed by running transfers
)

func getTransfer(id string) *Transfer {
	transfersMu.Lock()
	defer transfersMu.Unlock()
	return transfers[id]
}

func register(t *Transfer) {
	t.active = time.Now()
	transfersMu.Lock()
	transfers[t.id] = t
	transfersMu.Unlock()
}

func startRx(m *Message) {
	register(&Transfer{
		id: m.Transfer.ID, chat: m.Chat, msgID: m.ID, from: m.From,
		total: m.Transfer.Total, count: m.Transfer.Count,
		files: map[string]*rxFile{}, completed: map[string]bool{}, roots: map[string]string{},
	})
	if m.Transfer.Count == 0 {
		getTransfer(m.Transfer.ID).finish("done", "")
	}
}

func startTx(m *Message, peerIDs []string) *Transfer {
	t := &Transfer{
		id: m.Transfer.ID, chat: m.Chat, msgID: m.ID, from: m.From, outgoing: true,
		total: m.Transfer.Total * int64(len(peerIDs)), count: m.Transfer.Count, peers: peerIDs,
	}
	register(t)
	return t
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

var bufPool = sync.Pool{New: func() any { b := make([]byte, 1<<20); return &b }}

func (t *Transfer) receiveChunk(rel string, size, off, length int64, body io.Reader) error {
	rf, err := t.openRx(rel, size)
	if err != nil || rf == nil {
		return err // rf == nil: file already complete (duplicate chunk)
	}
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	n, err := io.CopyBuffer(io.NewOffsetWriter(rf.f, off), countingReader{body, &t.done}, *bp)
	if err != nil {
		return err
	}
	if length >= 0 && n != length {
		return fmt.Errorf("short chunk: %d of %d bytes", n, length)
	}
	if off+n > size {
		return errors.New("chunk past end of file")
	}
	return t.chunkDone(rel, rf, n)
}

func (t *Transfer) openRx(rel string, size int64) (*rxFile, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return nil, errFinished
	}
	if t.completed[rel] {
		return nil, nil
	}
	if rf := t.files[rel]; rf != nil {
		return rf, nil
	}
	final, err := t.destPath(rel)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return nil, err
	}
	part := final + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	if size > 0 {
		_ = f.Truncate(size) // reserve space up front; also faster on Windows
	}
	rf := &rxFile{f: f, part: part, final: final, size: size}
	t.files[rel] = rf
	if t.firstPath == "" {
		t.firstPath = final
	}
	return rf, nil
}

func (t *Transfer) chunkDone(rel string, rf *rxFile, n int64) error {
	t.mu.Lock()
	rf.got += n
	if rf.got < rf.size || t.completed[rel] {
		t.mu.Unlock()
		return nil
	}
	delete(t.files, rel)
	t.completed[rel] = true
	t.mu.Unlock()

	err := rf.f.Close()
	if err == nil {
		err = os.Rename(rf.part, rf.final)
	}
	if err != nil {
		t.finish("failed", err.Error())
		return err
	}
	t.mu.Lock()
	t.filesDone++
	all := t.filesDone >= t.count
	t.mu.Unlock()
	if all {
		t.finish("done", "")
	}
	return nil
}

// destPath maps a sender path like "Photos/2024/a.jpg" into the download
// folder. The top-level name gets " (2)" etc. if it already exists, so
// nothing is ever overwritten. Caller holds t.mu.
func (t *Transfer) destPath(rel string) (string, error) {
	parts := sanitizeRel(rel)
	if len(parts) == 0 {
		return "", errors.New("bad file name")
	}
	dl := getCfg().DownloadDir
	if err := os.MkdirAll(dl, 0o755); err != nil {
		return "", err
	}
	top := parts[0]
	if mapped, ok := t.roots[top]; ok {
		parts[0] = mapped
	} else {
		u := uniqueName(dl, top, len(parts) == 1)
		t.roots[top] = u
		parts[0] = u
	}
	return filepath.Join(append([]string{dl}, parts...)...), nil
}

func uniqueName(dir, name string, isFile bool) string {
	transfersMu.Lock()
	defer transfersMu.Unlock()
	free := func(n string) bool {
		p := filepath.Join(dir, n)
		if reserved[strings.ToLower(p)] {
			return false
		}
		_, e1 := os.Lstat(p)
		_, e2 := os.Lstat(p + ".part")
		return os.IsNotExist(e1) && os.IsNotExist(e2)
	}
	cand := name
	stem, ext := name, ""
	if isFile {
		ext = filepath.Ext(name)
		stem = strings.TrimSuffix(name, ext)
	}
	for i := 2; !free(cand); i++ {
		cand = fmt.Sprintf("%s (%d)%s", stem, i, ext)
	}
	reserved[strings.ToLower(filepath.Join(dir, cand))] = true
	return cand
}

var winReserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true}

// sanitizeRel splits a "/"-separated relative path into safe components that
// are valid on both Windows and macOS and cannot escape the download folder.
func sanitizeRel(rel string) []string {
	rel = strings.ReplaceAll(rel, "\\", "/")
	var out []string
	for _, p := range strings.Split(rel, "/") {
		p = strings.Map(func(r rune) rune {
			if r < 32 || strings.ContainsRune(`<>:"|?*`, r) || r == unicode.ReplacementChar {
				return '_'
			}
			return r
		}, p)
		p = strings.TrimRight(p, " .")
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." {
			continue
		}
		base := strings.ToUpper(strings.SplitN(p, ".", 2)[0])
		if winReserved[base] {
			p = "_" + p
		}
		if len(p) > 200 {
			ext := filepath.Ext(p)
			if len(ext) > 20 {
				ext = ""
			}
			p = p[:200-len(ext)] + ext
		}
		out = append(out, p)
	}
	return out
}

// finish ends a transfer once; later calls are ignored.
func (t *Transfer) finish(status, errMsg string) {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	t.finished = true
	open := t.files
	t.files = map[string]*rxFile{}
	saved := ""
	if !t.outgoing {
		dl := getCfg().DownloadDir
		switch {
		case t.count == 1 && t.firstPath != "":
			saved = t.firstPath
		case len(t.roots) == 1:
			for _, r := range t.roots {
				saved = filepath.Join(dl, r)
			}
		default:
			saved = dl
		}
	}
	roots := t.roots
	t.mu.Unlock()

	for _, rf := range open {
		rf.f.Close()
		os.Remove(rf.part)
	}
	transfersMu.Lock()
	dl := getCfg().DownloadDir
	for _, r := range roots {
		delete(reserved, strings.ToLower(filepath.Join(dl, r)))
	}
	transfersMu.Unlock()

	if status == "cancelled" {
		// A cancelled send should leave no message and no half-written files.
		if !t.outgoing {
			dl := getCfg().DownloadDir
			for _, r := range roots {
				_ = os.RemoveAll(filepath.Join(dl, r))
			}
		}
		if _, ok := store.removeID(t.msgID); ok {
			hub.broadcast(map[string]any{"type": "delete", "chat": t.chat, "id": t.msgID})
		}
		time.AfterFunc(30*time.Second, func() {
			transfersMu.Lock()
			delete(transfers, t.id)
			transfersMu.Unlock()
		})
		return
	}
	done := t.done.Load()
	if status == "done" {
		done = t.total
	}
	u := store.update(t.chat, t.msgID, func(m *Message) {
		m.Transfer.Status = status
		m.Transfer.Error = errMsg
		if t.total > 0 {
			m.Transfer.Done = done * m.Transfer.Total / t.total
		}
		if saved != "" {
			m.Transfer.SavedPath = saved
		}
	})
	if u != nil {
		hub.broadcast(map[string]any{"type": "update", "msg": u})
	}
	if status == "done" && !t.outgoing {
		logger.Printf("received %s from %s -> %s", t.id, t.from, saved)
	}
	// Leave it registered briefly so late duplicate chunks get a clean 410.
	time.AfterFunc(30*time.Second, func() {
		transfersMu.Lock()
		delete(transfers, t.id)
		transfersMu.Unlock()
	})
}

// runProgressTicker pushes progress + speed for active transfers to the UI.
func runProgressTicker() {
	const every = 400 * time.Millisecond
	for range time.Tick(every) {
		transfersMu.Lock()
		list := make([]*Transfer, 0, len(transfers))
		for _, t := range transfers {
			list = append(list, t)
		}
		transfersMu.Unlock()
		for _, t := range list {
			t.mu.Lock()
			if t.finished {
				t.mu.Unlock()
				continue
			}
			d := t.done.Load()
			if d > t.total {
				d = t.total
			}
			if d != t.lastDone {
				t.active = time.Now()
			} else if time.Since(t.active) > stallTimeout {
				// Sender closed/crashed or the network dropped for good.
				t.mu.Unlock()
				go t.finish("failed", "connection lost")
				continue
			}
			inst := float64(d-t.lastDone) / every.Seconds()
			t.speed = t.speed*0.6 + inst*0.4
			t.lastDone = d
			speed := t.speed
			t.mu.Unlock()
			hub.broadcast(map[string]any{"type": "progress", "tid": t.id, "chat": t.chat, "done": d, "total": t.total, "speed": int64(speed)})
		}
	}
}
