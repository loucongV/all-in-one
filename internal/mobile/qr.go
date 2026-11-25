package mobile

import (
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// RenderTerminalQR encodes content into a compact, high-contrast Unicode half-block QR code
// suitable for rendering inside a terminal modal (2 QR rows per terminal line).
func RenderTerminalQR(content string) (string, error) {
	qr, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return "", err
	}
	qr.DisableBorder = false
	bm := qr.Bitmap()
	if len(bm) == 0 {
		return "", nil
	}

	// Render with explicit black foreground on white background (\x1b[30;47m) so phone cameras
	// scan reliably regardless of dark/light terminal themes.
	const (
		fgBlackBgWhite = "\x1b[30;47m"
		reset          = "\x1b[0m"
	)

	rows := len(bm)
	cols := len(bm[0])
	var out strings.Builder
	for y := 0; y < rows; y += 2 {
		out.WriteString(fgBlackBgWhite)
		for x := 0; x < cols; x++ {
			top := bm[y][x]
			bot := false
			if y+1 < rows {
				bot = bm[y+1][x]
			}
			switch {
			case top && bot:
				out.WriteRune('█')
			case top && !bot:
				out.WriteRune('▀')
			case !top && bot:
				out.WriteRune('▄')
			default:
				out.WriteRune(' ')
			}
		}
		out.WriteString(reset)
		if y+2 < rows {
			out.WriteByte('\n')
		}
	}
	return out.String(), nil
}
