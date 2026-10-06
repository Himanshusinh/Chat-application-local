// mkicon draws the app icon (a chat bubble on a blue rounded square) and
// writes icon.png (1024px) plus icon.ico for Windows. Usage: mkicon <outdir>
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func clamp(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// sdRoundRect: signed distance to a rounded rectangle centred at (cx,cy).
func sdRoundRect(x, y, cx, cy, hw, hh, r float64) float64 {
	qx := math.Abs(x-cx) - hw + r
	qy := math.Abs(y-cy) - hh + r
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

func draw1024() *image.RGBA {
	const S = 1024.0
	img := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	for py := 0; py < 1024; py++ {
		for px := 0; px < 1024; px++ {
			x, y := float64(px)+.5, float64(py)+.5
			// Background squircle with a diagonal blue gradient (macOS-style margin).
			bg := clamp(.5 - sdRoundRect(x, y, S/2, S/2, 412, 412, 190))
			r, g, b := 0.102, 0.451, 0.910 // #1a73e8
			// Chat bubble: rounded rect + tail.
			bub := sdRoundRect(x, y, 512, 470, 250, 190, 120)
			tx, ty := x-360, y-680 // tail triangle region
			tail := -1.0
			if ty > -40 && ty < 90 && tx > -40 && tx < 120 && tx < 120-ty*1.1 && tx > -ty*0.2-40 {
				tail = 1
			}
			wb := clamp(.5 - bub)
			if tail > 0 {
				wb = 1
			}
			// Three dots.
			dots := 0.0
			for _, cx := range []float64{392, 512, 632} {
				dots = math.Max(dots, clamp(.5-(math.Hypot(x-cx, y-470)-42)))
			}
			cr, cg, cb := r, g, b
			cr, cg, cb = cr*(1-wb)+wb, cg*(1-wb)+wb, cb*(1-wb)+wb
			cr, cg, cb = cr*(1-dots)+r*dots, cg*(1-dots)+g*dots, cb*(1-dots)+b*dots
			img.SetRGBA(px, py, color.RGBA{uint8(cr * bg * 255), uint8(cg * bg * 255), uint8(cb * bg * 255), uint8(bg * 255)})
		}
	}
	return img
}

func scaled(src image.Image, n int) []byte {
	// Box-filter downscale (premultiplied RGBA, so edges stay clean).
	sb := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, n, n))
	f := float64(sb.Dx()) / float64(n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			x0, x1 := int(float64(x)*f), int(float64(x+1)*f)
			y0, y1 := int(float64(y)*f), int(float64(y+1)*f)
			var r, g, b, a, c uint32
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r, g, b, a, c = r+cr, g+cg, b+cb, a+ca, c+1
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / c >> 8), uint8(g / c >> 8), uint8(b / c >> 8), uint8(a / c >> 8)})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, dst)
	return buf.Bytes()
}

// writeICO packs PNG images into a .ico (supported since Windows Vista).
func writeICO(path string, pngs map[int][]byte, sizes []int) error {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, []uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for _, s := range sizes {
		w := uint8(s)
		if s >= 256 {
			w = 0
		}
		binary.Write(&buf, binary.LittleEndian, []uint8{w, w, 0, 0})
		binary.Write(&buf, binary.LittleEndian, []uint16{1, 32})
		binary.Write(&buf, binary.LittleEndian, []uint32{uint32(len(pngs[s])), uint32(offset)})
		offset += len(pngs[s])
	}
	for _, s := range sizes {
		buf.Write(pngs[s])
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// bmpEntry encodes an icon image in the classic 32-bit DIB format (what
// NSIS and very old Windows tools understand).
func bmpEntry(src image.Image, n int) []byte {
	var png8 []byte = scaled(src, n)
	img, _ := png.Decode(bytes.NewReader(png8))
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, []uint32{40, uint32(n), uint32(n * 2)})
	binary.Write(&buf, binary.LittleEndian, []uint16{1, 32})
	binary.Write(&buf, binary.LittleEndian, []uint32{0, 0, 0, 0, 0, 0})
	for y := n - 1; y >= 0; y-- { // bottom-up BGRA
		for x := 0; x < n; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a > 0 { // un-premultiply
				r, g, b = r*0xffff/a, g*0xffff/a, b*0xffff/a
			}
			buf.Write([]byte{uint8(b >> 8), uint8(g >> 8), uint8(r >> 8), uint8(a >> 8)})
		}
	}
	buf.Write(make([]byte, ((n+31)/32)*4*n)) // AND mask (alpha channel is used instead)
	return buf.Bytes()
}

func main() {
	out := "."
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	img := draw1024()
	var full bytes.Buffer
	_ = png.Encode(&full, img)
	must(os.WriteFile(filepath.Join(out, "icon.png"), full.Bytes(), 0o644))
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	pngs := map[int][]byte{}
	for _, s := range sizes {
		pngs[s] = scaled(img, s)
	}
	must(os.WriteFile(filepath.Join(out, "icon-256.png"), pngs[256], 0o644))
	must(writeICO(filepath.Join(out, "icon.ico"), pngs, sizes))
	bmps := map[int][]byte{}
	for _, n := range []int{16, 32, 48} {
		bmps[n] = bmpEntry(img, n)
	}
	must(writeICO(filepath.Join(out, "icon-installer.ico"), bmps, []int{16, 32, 48}))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
