package mobile

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func TestRenderTerminalQRAndBridgeAuthSecurity(t *testing.T) {
	qr, err := RenderTerminalQR("http://192.168.1.10:12345/?token=test-token")
	if err != nil {
		t.Fatalf("RenderTerminalQR failed: %v", err)
	}
	if !strings.Contains(qr, "█") && !strings.Contains(qr, "▀") && !strings.Contains(qr, "▄") {
		t.Fatalf("expected Unicode half-block QR output, got: %q", qr)
	}

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"path":"` + r.URL.Path + `"}`))
	})
	bridge := NewBridge(backend)
	bridge.SetVoiceEnabledFunc(func() bool { return true })
	st, err := bridge.Start(0, false, false)
	if err != nil {
		t.Fatalf("Bridge.Start failed: %v", err)
	}
	defer bridge.Stop()

	if !st.Running || st.HTTPS || st.Port == 0 || st.Token == "" {
		t.Fatalf("unexpected mobile status: %+v", st)
	}

	baseURL := "http://127.0.0.1:" + strings.Split(strings.TrimPrefix(st.URL, "http://"), "/")[0]
	// Replace advertised LAN IP with 127.0.0.1 for deterministic test dial.
	if idx := strings.LastIndex(baseURL, ":"); idx > 0 {
		baseURL = "http://127.0.0.1" + baseURL[idx:]
	}

	// 1. Unauthenticated request MUST be rejected with 401.
	resp, err := http.Get(baseURL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	// 2. Valid ?token=... on "/" sets HttpOnly cookie and renders embedded Mobile Web UI.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err = client.Get(baseURL + "/?token=" + st.Token)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "AIO Mobile") {
		t.Fatalf("expected Mobile Web UI HTML, status=%d", resp.StatusCode)
	}

	// 3. Subsequent /api/state call authenticates cleanly via cookie.
	resp, err = client.Get(baseURL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "/api/state") {
		t.Fatalf("expected proxied /api/state, status=%d body=%s", resp.StatusCode, string(body))
	}

	// 4. Safe read-only capabilities endpoint works over mobile bridge.
	resp, err = client.Get(baseURL + "/api/mobile/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"voice_transcribe":true`) {
		t.Fatalf("expected capabilities JSON, status=%d body=%s", resp.StatusCode, string(body))
	}

	// 5. Destructive daemon-control routes MUST be blocked (403) over mobile bridge.
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/shutdown", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on /api/shutdown over mobile bridge, got %d", resp.StatusCode)
	}

	// 6. Rotating token immediately invalidates the old session cookie.
	st2, err := bridge.Start(0, true, false)
	if err != nil || st2.Token == st.Token {
		t.Fatalf("expected rotated token, err=%v", err)
	}
	resp, err = client.Get(baseURL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected old cookie to be rejected after token rotation, got %d", resp.StatusCode)
	}

	// 7. Switching to HTTPS serves valid self-signed TLS and advertises https:// URL.
	stTLS, err := bridge.Start(0, false, true)
	if err != nil {
		t.Fatalf("Bridge.Start(https=true) failed: %v", err)
	}
	if !stTLS.Running || !stTLS.HTTPS || !strings.HasPrefix(stTLS.URL, "https://") {
		t.Fatalf("expected HTTPS mobile status, got %+v", stTLS)
	}
	httpsBase := "https://127.0.0.1:" + strings.Split(strings.TrimPrefix(stTLS.URL, "https://"), "/")[0]
	if idx := strings.LastIndex(httpsBase, ":"); idx > 0 {
		httpsBase = "https://127.0.0.1" + httpsBase[idx:]
	}
	// Verify direct 200 OK on HTTPS /?token=... WITHOUT a 302 redirect (prevents Safari/Chrome self-signed cookie loss).
	noRedirectClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err = noRedirectClient.Get(httpsBase + "/?token=" + stTLS.Token)
	if err != nil {
		t.Fatalf("HTTPS GET failed: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "voiceBtn") || !strings.Contains(string(body), "fsBtn") || !strings.Contains(string(body), `href="/manifest.webmanifest?token=`) {
		t.Fatalf("expected direct 200 OK HTTPS mobile UI with voice, fullscreen & session-bound manifest, status=%d", resp.StatusCode)
	}
	// Also verify Authorization Bearer works even with zero cookies.
	bReq, _ := http.NewRequest(http.MethodGet, httpsBase+"/api/state", nil)
	bReq.Header.Set("Authorization", "Bearer "+stTLS.Token)
	resp, err = noRedirectClient.Do(bReq)
	if err != nil {
		t.Fatalf("HTTPS Bearer /api/state failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with Bearer token over HTTPS without cookies, got %d", resp.StatusCode)
	}
}

func TestMobileSessionTrackingAndSelectiveRevocation(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	bridge := NewBridge(backend)
	st, err := bridge.Start(0, false, false)
	if err != nil {
		t.Fatalf("Bridge.Start: %v", err)
	}
	defer bridge.Stop()

	baseURL := "http://127.0.0.1:" + strings.Split(strings.TrimPrefix(st.URL, "http://"), "/")[0]
	if idx := strings.LastIndex(baseURL, ":"); idx > 0 {
		baseURL = "http://127.0.0.1" + baseURL[idx:]
	}

	loginDevice := func(ua string) (*http.Client, string) {
		jar, _ := cookiejar.New(nil)
		cl := &http.Client{Jar: jar}
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/?token="+st.Token, nil)
		req.Header.Set("User-Agent", ua)
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("login %s: %v", ua, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", ua, resp.StatusCode)
		}
		html := string(body)
		if !strings.Contains(html, `<meta name="aio-session" content="`) {
			t.Fatalf("expected injected aio-session meta tag in HTML")
		}
		return cl, html
	}

	iphoneClient, _ := loginDevice("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")
	androidClient, _ := loginDevice("Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Mobile Safari/537.36")

	cur := bridge.Status()
	if len(cur.Sessions) != 2 {
		t.Fatalf("expected 2 tracked mobile sessions, got %d (%+v)", len(cur.Sessions), cur.Sessions)
	}

	var iphoneID, androidID string
	for _, s := range cur.Sessions {
		if strings.Contains(s.Device, "iPhone") {
			iphoneID = s.ID
		} else if strings.Contains(s.Device, "Android") {
			androidID = s.ID
		}
	}
	if iphoneID == "" || androidID == "" {
		t.Fatalf("expected parsed iPhone & Android device labels, got %+v", cur.Sessions)
	}

	// Revoking ONLY iPhone must reject iPhone (401) while keeping Android alive (200).
	afterRevoke := bridge.RevokeSession(iphoneID)
	if len(afterRevoke.Sessions) != 1 || afterRevoke.Sessions[0].ID != androidID {
		t.Fatalf("expected only Android session remaining, got %+v", afterRevoke.Sessions)
	}

	resp, err := iphoneClient.Get(baseURL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected revoked iPhone session to get 401, got %d", resp.StatusCode)
	}

	resp, err = androidClient.Get(baseURL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected remaining Android session to stay 200, got %d", resp.StatusCode)
	}

	// Public home-screen icons must be served without auth; manifest must embed the session secret.
	resp, err = http.Get(baseURL + "/apple-touch-icon.png")
	if err != nil {
		t.Fatal(err)
	}
	iconBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(iconBytes) < 8 || string(iconBytes[1:4]) != "PNG" {
		t.Fatalf("expected public PNG apple-touch-icon, status=%d len=%d", resp.StatusCode, len(iconBytes))
	}
	resp, err = androidClient.Get(baseURL + "/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(manifest), `"display":"standalone"`) || !strings.Contains(string(manifest), `"start_url":"/?token=`) {
		t.Fatalf("expected session-bound standalone manifest, status=%d body=%s", resp.StatusCode, manifest)
	}
	// Unauthenticated root returns a friendly expired-session HTML page.
	resp, err = http.Get(baseURL + "/?token=bogus")
	if err != nil {
		t.Fatal(err)
	}
	expiredBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(expiredBody), "重新扫码") {
		t.Fatalf("expected friendly 401 HTML for expired session, status=%d", resp.StatusCode)
	}

	// RevokeAllSessions must invalidate Android too while keeping the bridge running.
	afterAll := bridge.RevokeAllSessions()
	if !afterAll.Running || len(afterAll.Sessions) != 0 {
		t.Fatalf("expected running bridge with 0 sessions after RevokeAllSessions, got %+v", afterAll)
	}
	resp, err = androidClient.Get(baseURL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected Android session to get 401 after RevokeAllSessions, got %d", resp.StatusCode)
	}
}
