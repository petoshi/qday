package qday

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
)

//go:embed web/*
var webFiles embed.FS

// Handler restricts the signing and mining API to a loopback listener, a local
// Host, a bearer token and same-origin requests. Launch codes are single use;
// the persistent API token is never put in a URL.
func (s *Service) Handler(token string, listenAddress string) http.Handler {
	_, port, _ := net.SplitHostPort(listenAddress)
	expected := sha256.Sum256([]byte(token))
	files, _ := fs.Sub(webFiles, "web")
	static := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		host, p, err := net.SplitHostPort(r.Host)
		if err != nil || p != port || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
			http.Error(w, "invalid local host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || u.Scheme != "http" {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			static.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/session" {
			if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "invalid session request", http.StatusBadRequest)
				return
			}
			var req struct {
				Code string `json:"code"`
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
			d.DisallowUnknownFields()
			if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid session request", http.StatusBadRequest)
				return
			}
			token, ok := s.browser.exchange(req.Code, time.Now())
			if !ok {
				http.Error(w, "launch code expired or already used; reopen QDAY Wallet", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"token": token})
			return
		}
		if r.URL.Path == "/api/network-status" {
			if r.Method != http.MethodGet {
				http.Error(w, "GET required", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s.networkStatus())
			return
		}
		got := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || (subtle.ConstantTimeCompare(got[:], expected[:]) != 1 && !s.browser.valid(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), time.Now())) {
			http.Error(w, "local access token required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		respond := func(v any, err error) {
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(v)
		}
		if r.Method == "GET" && r.URL.Path == "/api/status" {
			v, err := s.Status()
			respond(v, err)
			return
		}
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "POST application/json required", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Peer           string `json:"peer"`
			ReplaceAddress string `json:"replaceAddress"`
			FromAddress    string `json:"fromAddress"`
			Password       string `json:"password"`
			Phrase         string `json:"phrase"`
			Threads        int    `json:"threads"`
			Address        string `json:"address"`
			Amount         string `json:"amount"`
			Witness        string `json:"witness"`
			Unit           string `json:"unit"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			respond(nil, errors.New("invalid request body"))
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			respond(nil, errors.New("trailing request body"))
			return
		}
		switch r.URL.Path {
		case "/api/launch":
			respond(map[string]string{"url": "http://" + r.Host + "/#launch=" + s.browser.launchCode(time.Now())}, nil)
		case "/api/shutdown":
			respond(map[string]bool{"ok": true}, nil)
			time.AfterFunc(150*time.Millisecond, s.cancel)
		case "/api/restart":
			if err := s.RequestRestart(); err != nil {
				respond(nil, err)
				return
			}
			respond(map[string]bool{"ok": true}, nil)
			time.AfterFunc(150*time.Millisecond, s.cancel)
		case "/api/peers":
			result, err := s.AddPeer(r.Context(), body.Peer)
			respond(result, err)
		case "/api/create":
			phrase, err := s.Create(r.Context(), body.Password, body.Phrase)
			respond(map[string]string{"phrase": phrase}, err)
		case "/api/restore":
			address, err := s.Restore(r.Context(), body.Password, body.Phrase, body.ReplaceAddress)
			respond(map[string]string{"address": address}, err)
		case "/api/unlock":
			respond(map[string]bool{"ok": true}, s.Unlock(r.Context(), body.Password))
		case "/api/recovery":
			phrase, err := s.Recovery(body.Password)
			respond(map[string]string{"phrase": phrase}, err)
		case "/api/lock":
			s.Lock()
			respond(map[string]bool{"ok": true}, nil)
		case "/api/start":
			respond(map[string]bool{"ok": true}, s.Start(body.Threads))
		case "/api/stop":
			s.Stop()
			respond(map[string]bool{"ok": true}, nil)
		case "/api/defend":
			id, err := s.Transfer(r.Context(), types.QdayAddress{}, types.ZeroCurrency, true)
			respond(map[string]any{"transaction": id}, err)
		case "/api/send":
			to, err := types.ParseQdayAddress(strings.TrimSpace(body.Address))
			if err != nil {
				respond(nil, err)
				return
			}
			cs := s.CM.TipState()
			if body.Unit != cs.QdayUnits(cs.Index.Height).ExactString() {
				respond(nil, errors.New("QDAY denomination changed; review the amount again"))
				return
			}
			amount, err := ParseAmount(body.Amount, cs.QdayUnits(cs.Index.Height))
			if err != nil {
				respond(nil, err)
				return
			}
			id, err := s.submitFor(r.Context(), body.FromAddress, to, amount, false, nil)
			respond(map[string]any{"transaction": id}, err)
		case "/api/proof", "/api/proof/verify":
			var witness [32]byte
			b, err := hex.DecodeString(body.Witness)
			if err != nil || len(b) != 32 {
				respond(nil, errors.New("witness must be a canonical scalar encoded as 32 little-endian bytes"))
				return
			}
			copy(witness[:], b)
			cs := s.CM.TipState()
			if !consensus.VerifyQdayProof(cs.Network.Qday.Canary, witness) {
				respond(nil, errors.New("solution does not solve this network's challenge"))
				return
			}
			if r.URL.Path == "/api/proof/verify" {
				unit := cs.QdayUnits(cs.Index.Height)
				respond(map[string]any{"valid": true, "network": cs.Network.Name, "fee": FormatAmount(cs.Network.Qday.ProofFee, unit), "unit": unit.ExactString(), "activationDelay": cs.Network.Qday.ActivationDelay, "qdayHeight": cs.QdayHeight, "proofPending": s.pendingProof()}, nil)
				return
			}
			if body.Unit != cs.QdayUnits(cs.Index.Height).ExactString() {
				respond(nil, errors.New("QDAY denomination changed; review the proof fee again"))
				return
			}
			id, err := s.submitFor(r.Context(), body.FromAddress, types.QdayAddress{}, types.ZeroCurrency, false, &witness)
			respond(map[string]any{"transaction": id}, err)
		default:
			http.NotFound(w, r)
		}
	})
}
