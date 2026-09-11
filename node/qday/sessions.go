package qday

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Browser launch codes are single use and expire quickly. The persistent local
// API credential never appears in browser URLs, process arguments or logs.
type browserSessions struct {
	mu     sync.Mutex
	codes  map[[32]byte]time.Time
	tokens map[[32]byte]time.Time
}

func randomCredential() string {
	var b [32]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func pruneCredentials(m map[[32]byte]time.Time, now time.Time, limit int) {
	for k, t := range m {
		if !now.Before(t) {
			delete(m, k)
		}
	}
	for len(m) >= limit {
		var oldest [32]byte
		var deadline time.Time
		for k, t := range m {
			if deadline.IsZero() || t.Before(deadline) {
				oldest = k
				deadline = t
			}
		}
		delete(m, oldest)
	}
}

func (s *browserSessions) launchCode(now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.codes == nil {
		s.codes = make(map[[32]byte]time.Time)
	}
	pruneCredentials(s.codes, now, 8)
	code := randomCredential()
	s.codes[sha256.Sum256([]byte(code))] = now.Add(time.Minute)
	return code
}

func (s *browserSessions) exchange(code string, now time.Time) (string, bool) {
	if len(code) != 64 {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := sha256.Sum256([]byte(code))
	deadline, ok := s.codes[h]
	delete(s.codes, h)
	if !ok || !now.Before(deadline) {
		return "", false
	}
	if s.tokens == nil {
		s.tokens = make(map[[32]byte]time.Time)
	}
	pruneCredentials(s.tokens, now, 32)
	token := randomCredential()
	s.tokens[sha256.Sum256([]byte(token))] = now.Add(24 * time.Hour)
	return token, true
}

func (s *browserSessions) valid(token string, now time.Time) bool {
	if len(token) != 64 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := sha256.Sum256([]byte(token))
	deadline, ok := s.tokens[h]
	if ok && !now.Before(deadline) {
		delete(s.tokens, h)
		return false
	}
	return ok
}
