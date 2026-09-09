//go:build windows

package durable

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openWindowsTestRoot(t *testing.T) (*Root, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, path
}

func TestWindowsDurableRoundTripAndContentBoundDelete(t *testing.T) {
	root, path := openWindowsTestRoot(t)
	if err := root.WriteAtomic("state.json", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteAtomic("state.json", []byte("second")); err != nil {
		t.Fatal(err)
	}
	payload, err := root.Read("state.json", int64(len("second")))
	if err != nil || string(payload) != "second" {
		t.Fatalf("Read() = %q, %v", payload, err)
	}
	if _, err := root.Read("state.json", 5); !errors.Is(err, ErrReadLimit) {
		t.Fatalf("bounded Read() error = %v", err)
	}
	if removed, err := root.Remove("state.json", sha256.Sum256([]byte("wrong"))); err == nil || removed {
		t.Fatalf("Remove(wrong digest) = %v, %v", removed, err)
	}
	if removed, err := root.Remove("state.json", sha256.Sum256([]byte("second"))); err != nil || !removed {
		t.Fatalf("Remove(exact digest) = %v, %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(path, "state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed file stat error = %v", err)
	}
}

func TestWindowsDurableRefusesUnsafeCoordinatesAndRootReplacement(t *testing.T) {
	root, path := openWindowsTestRoot(t)
	for _, name := range []string{"", ".", "..", "../escape", "child/file"} {
		if err := root.WriteAtomic(name, []byte("value")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("WriteAtomic(%q) error = %v", name, err)
		}
	}
	if _, err := root.Read("missing", -1); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Read(negative limit) error = %v", err)
	}
	if err := root.WriteAtomic("state", []byte("original")); err != nil {
		t.Fatal(err)
	}
	moved := path + "-moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Read("state", 64); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("Read(replaced root) error = %v", err)
	}
}

func TestWindowsDurableRejectsNonDirectoryAndClosedRoot(t *testing.T) {
	if _, err := OpenRoot("relative"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("OpenRoot(relative) error = %v", err)
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRoot(file); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("OpenRoot(file) error = %v", err)
	}
	root, _ := openWindowsTestRoot(t)
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exists("state"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Exists(closed) error = %v", err)
	}
}
