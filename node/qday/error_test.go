package qday

import (
	"errors"
	"testing"

	"go.sia.tech/coreutils/syncer"
)

func TestTransientRelayStateIsNotWalletError(t *testing.T) {
	s := newTestService(t)
	s.Manifest.Development = false
	s.setError(syncer.ErrNoPeers)
	if s.lastError != "" {
		t.Fatalf("transient peer state leaked into wallet error: %q", s.lastError)
	}

	s.setError(errors.New("test failure"))
	if s.lastError != "test failure" {
		t.Fatalf("real error was discarded: %q", s.lastError)
	}
}

func TestRelayFailureClearsOnRetry(t *testing.T) {
	s := newTestService(t)
	s.setRelayError(errors.New("connection timed out"))
	if s.lastError != "" || s.lastRelayError == "" {
		t.Fatal("relay failure became a wallet failure or disappeared from diagnostics")
	}
	s.setRelayError(nil)
	if s.lastRelayError != "" {
		t.Fatal("successful retry left a stale connectivity error")
	}
}
