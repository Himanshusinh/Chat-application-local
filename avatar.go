package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// A custom picture is stored as avatar.jpg and announced as "img:<hash>".
// The hash is short, so it fits in the LAN broadcast. The bytes themselves
// are fetched from the computer that owns the picture.

func isPhotoAvatar(s string) bool {
	return strings.HasPrefix(s, "img:") && len(s) > 4 && len(s) < 40
}

func avatarFile() string { return filepath.Join(dataDir, "avatar.jpg") }

func apiAvatar(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		apiAvatarSet(w, r)
		return
	}
	id := r.URL.Query().Get("id")
	me := getCfg().ID
	if id == "" || id == "me" || id == me {
		if !isPhotoAvatar(getCfg().Avatar) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeFile(w, r, avatarFile())
		return
	}
	p, ok := peers.get(id)
	if !ok || !isPhotoAvatar(p.Avatar) {
		http.NotFound(w, r)
		return
	}
	path, err := cachedPeerAvatar(id, strings.TrimPrefix(p.Avatar, "img:"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

func apiAvatarSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Data string `json:"data"`
	}
	if err := readJSON(r, &req); err != nil || req.Data == "" {
		apiError(w, 400, errors.New("choose a picture"))
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil || len(raw) == 0 || len(raw) > 2<<20 {
		apiError(w, 400, errors.New("that picture could not be read"))
		return
	}
	jpg, err := normalizeAvatar(raw)
	if err != nil {
		apiError(w, 400, err)
		return
	}
	if err := os.WriteFile(avatarFile(), jpg, 0o644); err != nil {
		apiError(w, 500, errors.New("could not save the picture"))
		return
	}
	sum := sha256.Sum256(jpg)
	tag := "img:" + hex.EncodeToString(sum[:6])
	updateCfg(func(c *Config) { c.Avatar = tag })
	announceNow()
	apiState(w, r)
}

func normalizeAvatar(raw []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("use a PNG, JPEG or GIF")
	}
	img = scaleAvatar(img, 256)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
		return nil, errors.New("could not save the picture")
	}
	if buf.Len() > 200<<10 {
		return nil, errors.New("that picture is too big")
	}
	return buf.Bytes(), nil
}

func scaleAvatar(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	if w <= max && h <= max {
		return src
	}
	nw, nh := w, h
	if w >= h {
		nw = max
		nh = h * max / w
	} else {
		nh = max
		nw = w * max / h
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			dst.Set(x, y, src.At(b.Min.X+x*w/nw, b.Min.Y+y*h/nh))
		}
	}
	return dst
}

func p2pAvatar(w http.ResponseWriter, r *http.Request) {
	if !isPhotoAvatar(getCfg().Avatar) {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(avatarFile()); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, avatarFile())
}

var avatarFetchMu sync.Mutex

func cachedPeerAvatar(id, hash string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.ContainsAny(hash, `/\`) {
		return "", errors.New("bad id")
	}
	dir := filepath.Join(dataDir, "avatars")
	cache := filepath.Join(dir, id+"-"+hash+".jpg")
	if st, err := os.Stat(cache); err == nil && st.Size() > 0 {
		return cache, nil
	}
	avatarFetchMu.Lock()
	defer avatarFetchMu.Unlock()
	if st, err := os.Stat(cache); err == nil && st.Size() > 0 {
		return cache, nil
	}
	p, ok := peers.get(id)
	if !ok || !p.Online {
		return "", errors.New("not available")
	}
	req, err := http.NewRequest("GET", "http://"+p.Addr+"/p2p/avatar", nil)
	if err != nil {
		return "", err
	}
	setP2PHeaders(req)
	resp, err := ctlClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errors.New("no picture")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 200<<10))
	if err != nil || len(b) == 0 {
		return "", errors.New("no picture")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(cache, b, 0o644); err != nil {
		return "", err
	}
	return cache, nil
}
