//go:build !windows

package secretstore

import (
	"errors"
	"testing"
)

func TestUnsupportedProtector(t *testing.T) {
	unsupported := unsupportedProtector{}
	if _, err := unsupported.Protect([]byte("value")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Protect error = %v", err)
	}
	if _, err := unsupported.Unprotect([]byte("value")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Unprotect error = %v", err)
	}
}
