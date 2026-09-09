//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gcpauth

import (
	"os"
	"testing"
)

func restrictTestFileAccess(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func loosenTestFileAccess(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}
