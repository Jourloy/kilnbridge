package platform

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestExclusiveLockAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.lock")
	first, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := Lock(path); err == nil {
		second.Close()
		t.Fatal("two agents acquired the lock")
	}
	first.Close()
	next, err := Lock(path)
	if err != nil {
		t.Fatal("lock did not release:", err)
	}
	next.Close()
}

func TestProtectRoundTrip(t *testing.T) {
	input := []byte("a per-user secret")
	encrypted, err := Protect(input)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Unprotect(encrypted)
	if err != nil || !bytes.Equal(input, output) {
		t.Fatalf("secret did not round trip: %v", err)
	}
}
