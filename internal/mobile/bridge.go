package mobile

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/loucongV/all-in-one/internal/protocol"
)

//go:embed web/index.html
var webFS embed.FS

const authCookieName = "aio_mobile_token"

type mobileSession struct {
	id         string
	secret     string
	device     string
	remoteIP   string
	createdAt  time.Time
	lastSeenAt time.Time
	conns      map[net.Conn]struct{}
}

// Bridge exposes an on-demand, token-authenticated LAN HTTP/HTTPS + WebSocket bridge
// so phones on the same Wi-Fi/LAN can scan a QR code and control active aio runs in real time,
// while allowing the desktop TUI to inspect and revoke individual connected mobile sessions.
type Bridge struct {
	backend      http.Handler
	voiceEnabled func() bool
	onChange     func()

	mu           sync.Mutex
	running      bool
	useTLS       bool
	ln           net.Listener
	srv          *http.Server
	port         int
	token        string
	selectedIP   string
	lanIPs       []string
	sessions     map[string]*mobileSession // keyed by secret
	sessionByID  map[string]*mobileSession // keyed by short display ID
	lastNotifyAt time.Time
}

func NewBridge(backend http.Handler) *Bridge {
	return &Bridge{
		backend:     backend,
		sessions:    make(map[string]*mobileSession),
		sessionByID: make(map[string]*mobileSession),
	}
}

// SetVoiceEnabledFunc registers a callback indicating whether server-side audio transcription is configured.
func (b *Bridge) SetVoiceEnabledFunc(fn func() bool) {
	b.mu.Lock()
	b.voiceEnabled = fn
	b.mu.Unlock()
}

// SetOnChangeFunc registers a callback invoked when mobile sessions connect, disconnect, or are revoked.
func (b *Bridge) SetOnChangeFunc(fn func()) {
	b.mu.Lock()
	b.onChange = fn
	b.mu.Unlock()
}

func (b *Bridge) notifyChange(force bool) {
	b.mu.Lock()
	fn := b.onChange
	if fn == nil {
		b.mu.Unlock()
		return
	}
	now := time.Now()
	if !force && now.Sub(b.lastNotifyAt) < 10*time.Second {
		b.mu.Unlock()
		return
	}
	b.lastNotifyAt = now
	b.mu.Unlock()
	go fn()
}

// Start opens a LAN TCP listener (port 0 chooses an ephemeral free port) and generates a fresh token.
// If already running with the same TLS mode, keeps the listener and optionally rotates the token
// (which also revokes all previously authenticated mobile sessions).
// If useTLS changes, seamlessly restarts the listener on the requested protocol.
func (b *Bridge) Start(port int, rotateToken bool, useTLS bool) (protocol.MobileStatus, error) {
	b.mu.Lock()

	if b.running && b.useTLS == useTLS {
		var connsToClose []net.Conn
		if rotateToken {
			tok, err := generateToken()
			if err != nil {
				b.mu.Unlock()
				return protocol.MobileStatus{}, err
			}
			b.token = tok
			connsToClose = b.clearSessionsLocked()
		}
		b.refreshIPsLocked()
		st := b.statusLocked()
		b.mu.Unlock()
		closeConns(connsToClose)
		return st, nil
	}

	// Preserve existing token if switching HTTP<->HTTPS without explicit rotation.
	prevToken := b.token
	prevRunning := b.running
	prevSrv := b.srv
	prevLn := b.ln
	var connsToClose []net.Conn
	if rotateToken {
		connsToClose = b.clearSessionsLocked()
	}
	b.running = false
	b.srv = nil
	b.ln = nil
	b.port = 0
	b.mu.Unlock()

	closeConns(connsToClose)
	if prevRunning {
		if prevSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
			_ = prevSrv.Shutdown(ctx)
			cancel()
		}
		if prevLn != nil {
			_ = prevLn.Close()
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	tok := prevToken
	if tok == "" || rotateToken {
		var err error
		tok, err = generateToken()
		if err != nil {
			return protocol.MobileStatus{}, err
		}
	}

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	rawLn, err := net.Listen("tcp4", addr)
	if err != nil && port != 0 {
		// Fall back to an ephemeral port if the preferred port is already in use.
		rawLn, err = net.Listen("tcp4", "0.0.0.0:0")
	}
	if err != nil {
		return protocol.MobileStatus{}, fmt.Errorf("listen mobile bridge %s: %w", addr, err)
	}
	actualPort := rawLn.Addr().(*net.TCPAddr).Port

	b.refreshIPsLocked()
	var ln net.Listener = rawLn
	if useTLS {
		tlsCfg, err := generateSelfSignedTLS(b.lanIPs)
		if err != nil {
			_ = rawLn.Close()
			return protocol.MobileStatus{}, err
		}
		ln = tls.NewListener(rawLn, tlsCfg)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", b.handleRequest)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	b.ln = ln
	b.srv = srv
	b.port = actualPort
	b.token = tok
	b.useTLS = useTLS
	b.running = true

	go func() {
		_ = srv.Serve(ln)
	}()

	return b.statusLocked(), nil
}

// Stop gracefully shuts down the LAN mobile bridge, clears the active token, and disconnects all sessions.
func (b *Bridge) Stop() {
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return
	}
	srv := b.srv
	ln := b.ln
	connsToClose := b.clearSessionsLocked()
	b.running = false
	b.srv = nil
	b.ln = nil
	b.port = 0
	b.token = ""
	b.mu.Unlock()

	closeConns(connsToClose)
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	if ln != nil {
		_ = ln.Close()
	}
}

// SelectIP switches the advertised LAN IP used to build the QR pairing URL.
func (b *Bridge) SelectIP(ip string) protocol.MobileStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refreshIPsLocked()
	ip = strings.TrimSpace(ip)
	if ip != "" {
		b.selectedIP = ip
	}
	return b.statusLocked()
}

// RevokeSession invalidates one mobile client session by ID and immediately closes its active WebSockets.
func (b *Bridge) RevokeSession(sessionID string) protocol.MobileStatus {
	b.mu.Lock()
	var connsToClose []net.Conn
	if sess, ok := b.sessionByID[strings.TrimSpace(sessionID)]; ok {
		delete(b.sessionByID, sess.id)
		delete(b.sessions, sess.secret)
		for c := range sess.conns {
			connsToClose = append(connsToClose, c)
		}
		sess.conns = nil
	}
	st := b.statusLocked()
	b.mu.Unlock()

	closeConns(connsToClose)
	return st
}

// RevokeAllSessions disconnects and invalidates all logged-in mobile devices while keeping the bridge running.
func (b *Bridge) RevokeAllSessions() protocol.MobileStatus {
	b.mu.Lock()
	connsToClose := b.clearSessionsLocked()
	st := b.statusLocked()
	b.mu.Unlock()

	closeConns(connsToClose)
	return st
}

func (b *Bridge) clearSessionsLocked() []net.Conn {
	var conns []net.Conn
	for _, sess := range b.sessions {
		for c := range sess.conns {
			conns = append(conns, c)
		}
		sess.conns = nil
	}
	b.sessions = make(map[string]*mobileSession)
	b.sessionByID = make(map[string]*mobileSession)
	return conns
}

func closeConns(conns []net.Conn) {
	for _, c := range conns {
		_ = c.Close()
	}
}

// Status returns a thread-safe snapshot of the mobile bridge state and connected client sessions.
func (b *Bridge) Status() protocol.MobileStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		b.refreshIPsLocked()
	}
	return b.statusLocked()
}

func (b *Bridge) refreshIPsLocked() {
	ips := DiscoverLANIPs()
	b.lanIPs = ips
	if len(ips) == 0 {
		if b.selectedIP == "" {
			b.selectedIP = "127.0.0.1"
		}
		return
	}
	found := false
	for _, ip := range ips {
		if ip == b.selectedIP {
			found = true
			break
		}
	}
	if !found {
		b.selectedIP = ips[0]
	}
}

func (b *Bridge) statusLocked() protocol.MobileStatus {
	if !b.running {
		return protocol.MobileStatus{Running: false}
	}
	ips := append([]string(nil), b.lanIPs...)
	scheme := "http"
	if b.useTLS {
		scheme = "https"
	}
	urlStr := fmt.Sprintf("%s://%s:%d/?token=%s", scheme, b.selectedIP, b.port, b.token)
	sessions := make([]protocol.MobileSession, 0, len(b.sessionByID))
	for _, s := range b.sessionByID {
		sessions = append(sessions, protocol.MobileSession{
			ID:          s.id,
			Device:      s.device,
			RemoteIP:    s.remoteIP,
			CreatedAt:   s.createdAt,
			LastSeenAt:  s.lastSeenAt,
			ActiveConns: len(s.conns),
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		iOnline := sessions[i].ActiveConns > 0
		jOnline := sessions[j].ActiveConns > 0
		if iOnline != jOnline {
			return iOnline
		}
		if !sessions[i].LastSeenAt.Equal(sessions[j].LastSeenAt) {
			return sessions[i].LastSeenAt.After(sessions[j].LastSeenAt)
		}
		return sessions[i].ID < sessions[j].ID
	})
	return protocol.MobileStatus{
		Running:    true,
		HTTPS:      b.useTLS,
		Port:       b.port,
		Token:      b.token,
		SelectedIP: b.selectedIP,
		LANIPs:     ips,
		URL:        urlStr,
		Sessions:   sessions,
	}
}

func (b *Bridge) validatePairingTokenLocked(provided string) bool {
	if b.token == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(b.token)) == 1
}

// authenticate validates either an existing per-device session secret or the QR pairing token.
// On a valid pairing token, it creates (or reuses for direct API calls) a tracked mobileSession.
func (b *Bridge) authenticate(r *http.Request, queryTok, headerTok, cookieTok string, isRootPage bool) (*mobileSession, bool, bool) {
	now := time.Now()
	ip := extractClientIP(r)
	device := parseMobileDevice(r.UserAgent())

	b.mu.Lock()
	// 1. Check existing per-device session secrets first (cookie -> Bearer -> query).
	for _, cand := range []string{cookieTok, headerTok, queryTok} {
		if cand == "" {
			continue
		}
		if sess, ok := b.sessions[cand]; ok {
			sess.lastSeenAt = now
			if ip != "" {
				sess.remoteIP = ip
			}
			if device != "" {
				sess.device = device
			}
			b.mu.Unlock()
			return sess, false, true
		}
	}

	// 2. Validate pairing QR token for initial login or direct Bearer/query client access.
	pairingOK := b.validatePairingTokenLocked(queryTok) ||
		b.validatePairingTokenLocked(headerTok) ||
		b.validatePairingTokenLocked(cookieTok)
	if !pairingOK {
		b.mu.Unlock()
		return nil, false, false
	}

	// For non-HTML direct API/WS requests using the pairing token (without a session cookie),
	// coalesce repeated calls from the same IP+device into one tracked session.
	if !isRootPage {
		for _, existing := range b.sessionByID {
			if existing.remoteIP == ip && existing.device == device {
				existing.lastSeenAt = now
				b.mu.Unlock()
				return existing, false, true
			}
		}
	}

	secret, err := generateToken()
	if err != nil {
		b.mu.Unlock()
		return nil, false, false
	}
	sid := generateSessionID()
	sess := &mobileSession{
		id:         sid,
		secret:     secret,
		device:     device,
		remoteIP:   ip,
		createdAt:  now,
		lastSeenAt: now,
		conns:      make(map[net.Conn]struct{}),
	}
	b.sessions[secret] = sess
	b.sessionByID[sid] = sess
	b.mu.Unlock()
	return sess, true, true
}

func (b *Bridge) trackConn(sess *mobileSession, c net.Conn) net.Conn {
	b.mu.Lock()
	if _, stillActive := b.sessionByID[sess.id]; !stillActive {
		b.mu.Unlock()
		_ = c.Close()
		return c
	}
	if sess.conns == nil {
		sess.conns = make(map[net.Conn]struct{})
	}
	tc := &trackedConn{Conn: c, bridge: b, sess: sess}
	sess.conns[tc] = struct{}{}
	sess.lastSeenAt = time.Now()
	b.mu.Unlock()
	b.notifyChange(true)
	return tc
}

func (b *Bridge) untrackConn(sess *mobileSession, c net.Conn) {
	b.mu.Lock()
	changed := false
	if sess.conns != nil {
		if _, ok := sess.conns[c]; ok {
			delete(sess.conns, c)
			sess.lastSeenAt = time.Now()
			changed = true
		}
	}
	b.mu.Unlock()
	if changed {
		b.notifyChange(true)
	}
}

type trackedConn struct {
	net.Conn
	bridge *Bridge
	sess   *mobileSession
	once   sync.Once
}

func (tc *trackedConn) Close() error {
	tc.once.Do(func() {
		tc.bridge.untrackConn(tc.sess, tc)
	})
	return tc.Conn.Close()
}

type sessionHijackWriter struct {
	http.ResponseWriter
	bridge *Bridge
	sess   *mobileSession
}

func (w *sessionHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking not supported")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	return w.bridge.trackConn(w.sess, conn), rw, nil
}

func (w *sessionHijackWriter) Flush() {
	if fl, ok := w.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (b *Bridge) handleRequest(w http.ResponseWriter, r *http.Request) {
	// Security headers on all responses.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")

	// Public, non-sensitive brand icons (fetched by iOS/Android without credentials
	// when adding AIO to the home screen).
	if r.Method == http.MethodGet {
		iconSize := 0
		switch r.URL.Path {
		case "/apple-touch-icon.png", "/apple-touch-icon-precomposed.png":
			iconSize = 180
		case "/icon-192.png":
			iconSize = 192
		case "/icon-512.png":
			iconSize = 512
		case "/favicon.ico":
			iconSize = 64
		}
		if iconSize > 0 {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(renderAppIconPNG(iconSize))
			return
		}
	}

	// Extract token from query param, Authorization Bearer header, or HttpOnly session cookie.
	queryTok := strings.TrimSpace(r.URL.Query().Get("token"))
	headerTok := ""
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(auth, "Bearer ") {
		headerTok = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	cookieTok := ""
	if c, err := r.Cookie(authCookieName); err == nil {
		cookieTok = strings.TrimSpace(c.Value)
	}

	isRootPage := (r.URL.Path == "/" || r.URL.Path == "/index.html") && r.Method == http.MethodGet
	sess, created, authed := b.authenticate(r, queryTok, headerTok, cookieTok, isRootPage)
	if !authed || sess == nil {
		if isRootPage {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(expiredSessionHTML))
			return
		}
		http.Error(w, "unauthorized: invalid or expired aio mobile session", http.StatusUnauthorized)
		return
	}
	if created {
		b.notifyChange(true)
	} else {
		b.notifyChange(false)
	}

	// On "/" bind the device's dedicated session secret into an HttpOnly SameSite=Lax cookie
	// and inject a <meta name="aio-session"> tag into the HTML (200 OK, zero 302 redirect)
	// so Safari/Chrome self-signed HTTPS works reliably and each device can be revoked individually.
	if isRootPage {
		b.mu.Lock()
		isTLS := b.useTLS
		b.mu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name:     authCookieName,
			Value:    sess.secret,
			Path:     "/",
			MaxAge:   30 * 86400,
			HttpOnly: true,
			Secure:   isTLS,
			SameSite: http.SameSiteLaxMode,
		})

		data, err := webFS.ReadFile("web/index.html")
		if err != nil {
			http.Error(w, "mobile ui unavailable", http.StatusInternalServerError)
			return
		}
		metaTag := []byte(`<meta charset="utf-8">` + "\n" + `<meta name="aio-session" content="` + sess.secret + `">`)
		rendered := bytes.Replace(data, []byte(`<meta charset="utf-8">`), metaTag, 1)
		// Bind the per-device session into the manifest URL so "Add to Home Screen" launches
		// authenticated in standalone mode (no browser address bar / bottom toolbar).
		rendered = bytes.Replace(rendered,
			[]byte(`href="/manifest.webmanifest"`),
			[]byte(`href="/manifest.webmanifest?token=`+sess.secret+`"`), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(rendered)
		return
	}

	if r.URL.Path == "/manifest.webmanifest" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(buildWebManifest(sess.secret))
		return
	}

	// Safe read-only capabilities endpoint for the mobile web client.
	if r.URL.Path == "/api/mobile/capabilities" && r.Method == http.MethodGet {
		b.mu.Lock()
		isTLS := b.useTLS
		voiceFn := b.voiceEnabled
		b.mu.Unlock()
		voiceOK := voiceFn != nil && voiceFn()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"https":            isTLS,
			"voice_transcribe": voiceOK,
			"session_id":       sess.id,
		})
		return
	}

	// Defense-in-depth: block destructive daemon-control endpoints over the LAN bridge.
	if r.URL.Path == "/api/shutdown" || strings.HasPrefix(r.URL.Path, "/api/mobile/") {
		http.Error(w, "forbidden on mobile bridge", http.StatusForbidden)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
		sw := &sessionHijackWriter{ResponseWriter: w, bridge: b, sess: sess}
		b.backend.ServeHTTP(sw, r)
		return
	}

	http.NotFound(w, r)
}

const expiredSessionHTML = `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="theme-color" content="#090d16"><title>AIO Mobile</title>
<style>body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#090d16;color:#eef2f8;font-family:-apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;padding:24px;box-sizing:border-box}
.c{max-width:340px;text-align:center;background:#131b2e;border:1px solid rgba(255,255,255,.08);border-radius:18px;padding:26px 20px}
h3{margin:0 0 8px;font-size:16px}p{margin:0;color:#7c8ca6;font-size:13.5px;line-height:1.6}</style></head>
<body><div class="c"><h3>手机会话已失效</h3><p>二维码 Token 已刷新、设备被踢出或电脑端 aio 已重启。<br>请在电脑端按 <b>,</b> → <b>4</b> 重新扫码连接；<br>如已添加到主屏幕，扫码后重新添加一次即可。</p></div></body></html>`

func generateToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func generateSessionID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "ms-" + hex.EncodeToString(b[:])
}

func extractClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// parseMobileDevice converts a User-Agent string into a concise human-readable device label.
func parseMobileDevice(ua string) string {
	l := strings.ToLower(strings.TrimSpace(ua))
	if l == "" {
		return "未知设备"
	}

	osLabel := "未知设备"
	switch {
	case strings.Contains(l, "iphone"):
		osLabel = "iPhone"
	case strings.Contains(l, "ipad"):
		osLabel = "iPad"
	case strings.Contains(l, "harmonyos") || strings.Contains(l, "openharmony"):
		osLabel = "HarmonyOS"
	case strings.Contains(l, "android"):
		osLabel = "Android"
	case strings.Contains(l, "macintosh") || strings.Contains(l, "mac os x"):
		osLabel = "macOS"
	case strings.Contains(l, "windows"):
		osLabel = "Windows"
	case strings.Contains(l, "linux"):
		osLabel = "Linux"
	case strings.Contains(l, "curl/"):
		return "curl"
	case strings.Contains(l, "go-http-client"):
		return "Go Client"
	}

	browser := ""
	switch {
	case strings.Contains(l, "micromessenger"):
		browser = "微信"
	case strings.Contains(l, "dingtalk"):
		browser = "钉钉"
	case strings.Contains(l, "lark") || strings.Contains(l, "feishu"):
		browser = "飞书"
	case strings.Contains(l, "edg/") || strings.Contains(l, "edgios") || strings.Contains(l, "edga"):
		browser = "Edge"
	case strings.Contains(l, "firefox") || strings.Contains(l, "fxios"):
		browser = "Firefox"
	case strings.Contains(l, "crios") || strings.Contains(l, "chrome"):
		browser = "Chrome"
	case strings.Contains(l, "safari"):
		browser = "Safari"
	}

	if browser != "" {
		return osLabel + " · " + browser
	}
	return osLabel
}
