package qday

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBrowserSessionLifecycle(t *testing.T) {
	var sessions browserSessions
	now := time.Now()
	code := sessions.launchCode(now)
	if _, ok := sessions.exchange(strings.Repeat("0", 64), now); ok {
		t.Fatal("guessed launch code accepted")
	}
	token, ok := sessions.exchange(code, now)
	if !ok || !sessions.valid(token, now) {
		t.Fatal("valid launch failed")
	}
	if _, ok = sessions.exchange(code, now); ok {
		t.Fatal("launch code replay accepted")
	}
	if sessions.valid(token, now.Add(24*time.Hour)) {
		t.Fatal("expired browser session accepted")
	}
	expired := sessions.launchCode(now)
	if _, ok = sessions.exchange(expired, now.Add(time.Minute)); ok {
		t.Fatal("expired launch code accepted")
	}
}

func TestAutomaticBrowserAuthentication(t *testing.T) {
	s := newTestService(t)
	root := strings.Repeat("a", 64)
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = s.Handler(root, server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	post := func(path, token, body, origin string) (int, map[string]string) {
		t.Helper()
		req, _ := http.NewRequest("POST", server.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v map[string]string
		_ = json.NewDecoder(res.Body).Decode(&v)
		return res.StatusCode, v
	}
	if code, _ := post("/api/launch", "", "{}", ""); code != 401 {
		t.Fatal("unauthenticated launch accepted")
	}
	code, v := post("/api/launch", root, "{}", "")
	if code != 200 {
		t.Fatal("launcher could not obtain code")
	}
	if strings.Contains(v["url"], root) {
		t.Fatal("persistent API token leaked into launch URL")
	}
	parts := strings.Split(v["url"], "#launch=")
	if len(parts) != 2 {
		t.Fatal("missing code")
	}
	body := `{"code":"` + parts[1] + `"}`
	if code, _ = post("/api/session", "", body, "http://attacker.invalid"); code != 403 {
		t.Fatal("cross-origin session exchange accepted")
	}
	code, v = post("/api/session", "", body, server.URL)
	if code != 200 || v["token"] == root {
		t.Fatal("session exchange failed")
	}
	if code, _ = post("/api/session", "", body, ""); code != 401 {
		t.Fatal("HTTP code replay accepted")
	}
	if code, _ = post("/api/stop", v["token"], "{}", server.URL); code != 200 {
		t.Fatal("browser session could not control wallet")
	}
}
