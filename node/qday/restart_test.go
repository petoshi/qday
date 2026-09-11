package qday

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRestartHTTPAuthorizationAndSupervisor(t *testing.T) {
	s := newTestService(t)
	token := strings.Repeat("a", 64)
	handler := s.Handler(token, "127.0.0.1:19770")
	post := func(route, credential, origin string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:19770/api/"+route, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	for _, route := range []string{"restart", "peers"} {
		if got := post(route, "", ""); got != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s returned %d", route, got)
		}
		if got := post(route, token, "https://untrusted.example"); got != http.StatusForbidden {
			t.Fatalf("cross-origin %s returned %d", route, got)
		}
	}
	if got := post("restart", token, ""); got != http.StatusBadRequest || s.RestartRequested() {
		t.Fatal("standalone node accepted an unsupervised restart")
	}
	s.RestartEnabled = true
	if got := post("restart", token, ""); got != http.StatusOK || !s.RestartRequested() {
		t.Fatal("supervised restart was not accepted")
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("restart did not begin graceful shutdown")
	}
}
