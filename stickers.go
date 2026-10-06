package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// Stickers: built-in ones ("b:<name>") are drawn by the app itself, so only
// the name travels. Custom stickers are small images identified by their
// content hash; the bytes travel with the message once and are cached in
// <data>/stickers on every computer that receives them.

const maxStickerBytes = 2 << 20

type StickerRef struct {
	ID   string `json:"id"`
	Ext  string `json:"ext,omitempty"`
	Data string `json:"data,omitempty"` // base64, only on the wire
}

var (
	builtinRe   = regexp.MustCompile(`^b:[a-z0-9-]{1,32}$`)
	customRe    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	stickerExts = map[string]string{"png": "image/png", "gif": "image/gif", "webp": "image/webp", "jpg": "image/jpeg"}
	stickerMu   sync.Mutex
)

func stickerDir() string { return filepath.Join(dataDir, "stickers") }

func (s *StickerRef) custom() bool { return !builtinRe.MatchString(s.ID) }

func (s *StickerRef) valid() bool {
	if builtinRe.MatchString(s.ID) {
		return true
	}
	_, ok := stickerExts[s.Ext]
	return ok && customRe.MatchString(s.ID)
}

func (s *StickerRef) path() string { return filepath.Join(stickerDir(), s.ID+"."+s.Ext) }

// storeStickerBytes saves sticker bytes, returning the content-hash ID.
func storeStickerBytes(b []byte, ext string) (*StickerRef, error) {
	if _, ok := stickerExts[ext]; !ok {
		return nil, errors.New("stickers must be PNG, GIF, WebP or JPG")
	}
	if len(b) == 0 || len(b) > maxStickerBytes {
		return nil, errors.New("sticker is too big (max 2 MB)")
	}
	sum := sha256.Sum256(b)
	ref := &StickerRef{ID: hex.EncodeToString(sum[:16]), Ext: ext}
	_ = os.MkdirAll(stickerDir(), 0o755)
	if _, err := os.Stat(ref.path()); err != nil {
		writeFileAtomic(ref.path(), b)
	}
	return ref, nil
}

// acceptSticker validates an incoming sticker and caches its bytes.
func acceptSticker(s *StickerRef) error {
	if s == nil || !s.valid() {
		return errors.New("bad sticker")
	}
	if s.custom() {
		if _, err := os.Stat(s.path()); err != nil {
			b, err := base64.StdEncoding.DecodeString(s.Data)
			if err != nil {
				return errors.New("bad sticker data")
			}
			ref, err := storeStickerBytes(b, s.Ext)
			if err != nil {
				return err
			}
			if ref.ID != s.ID {
				return errors.New("sticker data does not match")
			}
		}
	}
	s.Data = ""
	return nil
}

// wireMsg returns the message as sent to peers (custom sticker bytes attached).
func wireMsg(m *Message) *Message {
	w := cloneMsg(m)
	w.Status = ""
	if m.Sticker != nil {
		st := *m.Sticker
		if st.custom() {
			if b, err := os.ReadFile(st.path()); err == nil {
				st.Data = base64.StdEncoding.EncodeToString(b)
			}
		}
		w.Sticker = &st
	}
	return w
}

// ---- "My stickers" collection

func myStickersPath() string { return filepath.Join(stickerDir(), "mine.json") }

func loadMyStickers() []StickerRef {
	var list []StickerRef
	if b, err := os.ReadFile(myStickersPath()); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	return list
}

func saveMyStickers(list []StickerRef) {
	_ = os.MkdirAll(stickerDir(), 0o755)
	b, _ := json.Marshal(list)
	writeFileAtomic(myStickersPath(), b)
}

func addMySticker(ref StickerRef) []StickerRef {
	stickerMu.Lock()
	defer stickerMu.Unlock()
	list := loadMyStickers()
	for _, s := range list {
		if s.ID == ref.ID {
			return list
		}
	}
	list = append([]StickerRef{{ID: ref.ID, Ext: ref.Ext}}, list...)
	saveMyStickers(list)
	return list
}

func apiStickers(w http.ResponseWriter, r *http.Request) {
	list := loadMyStickers()
	if list == nil {
		list = []StickerRef{}
	}
	writeJSON(w, list)
}

func apiStickerAdd(w http.ResponseWriter, r *http.Request) {
	var req struct{ Data, Ext string }
	if err := readJSON(r, &req); err != nil {
		apiError(w, 400, err)
		return
	}
	b, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		apiError(w, 400, errors.New("bad image"))
		return
	}
	ref, err := storeStickerBytes(b, req.Ext)
	if err != nil {
		apiError(w, 400, err)
		return
	}
	writeJSON(w, addMySticker(*ref))
}

func apiStickerSave(w http.ResponseWriter, r *http.Request) {
	var ref StickerRef
	if err := readJSON(r, &ref); err != nil || !ref.valid() || !ref.custom() {
		apiError(w, 400, errors.New("bad sticker"))
		return
	}
	if _, err := os.Stat(ref.path()); err != nil {
		apiError(w, 404, errors.New("sticker not found"))
		return
	}
	writeJSON(w, addMySticker(ref))
}

func apiStickerRemove(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	_ = readJSON(r, &req)
	stickerMu.Lock()
	list := loadMyStickers()
	out := list[:0]
	for _, s := range list {
		if s.ID != req.ID {
			out = append(out, s)
		}
	}
	saveMyStickers(out)
	stickerMu.Unlock()
	writeJSON(w, out)
}

func apiStickerFile(w http.ResponseWriter, r *http.Request) {
	ref := StickerRef{ID: r.URL.Query().Get("id"), Ext: r.URL.Query().Get("ext")}
	if !ref.valid() || !ref.custom() {
		http.Error(w, "bad sticker", 400)
		return
	}
	w.Header().Set("Content-Type", stickerExts[ref.Ext])
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, ref.path())
}
