package mobile

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"sync"
)

// appIconCache lazily renders and caches square PNG app icons (apple-touch-icon / PWA icons).
var appIconCache sync.Map // map[int][]byte

// renderAppIconPNG draws the AIO brand mark (sky diamond prism + emerald prompt underline on a
// deep slate background) as a full-bleed square PNG. iOS/Android apply their own corner masks.
func renderAppIconPNG(size int) []byte {
	if size < 32 {
		size = 32
	}
	if cached, ok := appIconCache.Load(size); ok {
		return cached.([]byte)
	}

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := color.RGBA{R: 9, G: 13, B: 22, A: 255}
	inner := color.RGBA{R: 15, G: 23, B: 42, A: 255}
	sky := color.RGBA{R: 56, G: 189, B: 248, A: 255}
	green := color.RGBA{R: 52, G: 211, B: 153, A: 255}
	white := color.RGBA{R: 248, G: 250, B: 252, A: 255}

	cx, cy := float64(size)/2, float64(size)/2
	outerR := float64(size) * 0.34
	stroke := float64(size) * 0.055
	if stroke < 2 {
		stroke = 2
	}
	absf := func(v float64) float64 {
		if v < 0 {
			return -v
		}
		return v
	}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			d := absf(fx-cx) + absf(fy-cy) // L1 distance -> diamond
			switch {
			case d <= outerR && d >= outerR-stroke:
				img.SetRGBA(x, y, sky)
			case d < outerR-stroke:
				img.SetRGBA(x, y, inner)
			default:
				img.SetRGBA(x, y, bg)
			}
		}
	}

	// Draw a ">" chevron and a "_" prompt underline inside the diamond.
	thick := stroke * 0.8
	chevX0 := cx - float64(size)*0.11
	chevYMid := cy
	chevH := float64(size) * 0.085
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			// Chevron: two segments from (chevX0, mid-chevH)->(chevX0+chevH, mid)->(chevX0, mid+chevH)
			dy := fy - chevYMid
			if absf(dy) <= chevH {
				lineX := chevX0 + (chevH - absf(dy))
				if absf(fx-lineX) <= thick/2 {
					img.SetRGBA(x, y, white)
				}
			}
			// Underline prompt
			ux0 := cx + float64(size)*0.02
			ux1 := cx + float64(size)*0.12
			uy := cy + chevH
			if fx >= ux0 && fx <= ux1 && absf(fy-uy) <= thick/2 {
				img.SetRGBA(x, y, green)
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	data := buf.Bytes()
	appIconCache.Store(size, data)
	return data
}

// buildWebManifest returns a Web App Manifest whose start_url embeds the device's own session
// secret, so a home-screen-installed AIO (iOS standalone storage is isolated from Safari cookies)
// still authenticates on launch while running with no address bar and no bottom browser toolbar.
func buildWebManifest(sessionSecret string) []byte {
	startURL := "/"
	if sessionSecret != "" {
		startURL = "/?token=" + sessionSecret
	}
	m := map[string]any{
		"name":             "AIO Mobile",
		"short_name":       "AIO",
		"id":               "/",
		"start_url":        startURL,
		"scope":            "/",
		"display":          "standalone",
		"display_override": []string{"fullscreen", "standalone"},
		"orientation":      "any",
		"background_color": "#090d16",
		"theme_color":      "#090d16",
		"icons": []map[string]string{
			{"src": "/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any maskable"},
			{"src": "/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any maskable"},
		},
	}
	b, _ := json.Marshal(m)
	return b
}
