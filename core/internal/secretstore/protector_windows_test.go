//go:build windows

package secretstore

import (
	"bytes"
	"errors"
	"testing"
)

func TestDPAPIProtectorRoundTrip(t *testing.T) {
	protector := defaultProtector()
	plaintext := []byte("agentbell-dpapi-round-trip")
	ciphertext, err := protector.Protect(plaintext)
	if err != nil {
		t.Fatalf("Protect: %v", err)
	}
	if len(ciphertext) == 0 || bytes.Equal(ciphertext, plaintext) {
		t.Fatal("DPAPI did not produce opaque ciphertext")
	}
	restored, err := protector.Unprotect(ciphertext)
	if err != nil {
		t.Fatalf("Unprotect: %v", err)
	}
	if !bytes.Equal(restored, plaintext) {
		t.Fatalf("round trip = %q, want %q", restored, plaintext)
	}
}

func TestDPAPIProtectorRejectsInvalidBlobs(t *testing.T) {
	protector := dpapiProtector{}
	for _, value := range [][]byte{nil, make([]byte, maximumBlobBytes+1)} {
		if _, err := protector.Protect(value); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("Protect error = %v, want ErrInvalidSecret", err)
		}
	}
	if _, err := protector.Unprotect([]byte("not-a-dpapi-blob")); !errors.Is(err, ErrBackend) {
		t.Fatalf("Unprotect error = %v, want ErrBackend", err)
	}
}
