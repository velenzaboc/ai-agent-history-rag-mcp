//go:build darwin || linux

package watch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixPlatformRootRejectsUnboundAndMissingNamedRoot(t *testing.T) {
	var unbound platformRoot
	if _, err := unbound.verify("/missing"); !errors.Is(err, ErrRootUnbound) {
		t.Fatalf("unbound verify error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	var root platformRoot
	bound, err := root.bind(path)
	if err != nil || !bound {
		t.Fatalf("bind = %v, %v", bound, err)
	}
	t.Cleanup(func() { _ = root.close() })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := root.verify(path); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("missing named root verify error = %v", err)
	}
}
