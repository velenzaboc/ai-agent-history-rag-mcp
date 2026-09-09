package durable

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingRandomReader struct{}

func (failingRandomReader) Read([]byte) (int, error) { return 0, errors.New("randomness unavailable") }

type zeroThenFailRandomReader struct{ reads int }

func (r *zeroThenFailRandomReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		return len(p), nil
	}
	return 0, errors.New("randomness unavailable")
}

func openTestRoot(t *testing.T) (*Root, string) {
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

func TestAtomicWriteReadAndBoundedDelete(t *testing.T) {
	root, path := openTestRoot(t)
	if err := root.WriteAtomic("state.json", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteAtomic("state.json", []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, err := root.Read("state.json", int64(len("second")))
	if err != nil || string(got) != "second" {
		t.Fatalf("Read() = %q, %v", got, err)
	}
	if _, err := root.Read("state.json", 5); !errors.Is(err, ErrReadLimit) {
		t.Fatalf("Read(limit) = %v", err)
	}
	if removed, err := root.Remove("state.json", sha256.Sum256([]byte("wrong"))); err == nil || removed {
		t.Fatalf("Remove(wrong digest) = %v, %v", removed, err)
	}
	if removed, err := root.Remove("state.json", sha256.Sum256([]byte("second"))); err != nil || !removed {
		t.Fatalf("Remove(exact digest) = %v, %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(path, "state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed file stat = %v", err)
	}
}

func TestDurableRejectsUnsafeCoordinatesAndMissingObjects(t *testing.T) {
	root, _ := openTestRoot(t)
	if removed, err := root.Remove("missing", sha256.Sum256([]byte("missing"))); err != nil || removed {
		t.Fatalf("Remove(missing) = %v, %v", removed, err)
	}
	for _, name := range []string{"", ".", "..", "../escape", "child/file"} {
		if _, err := root.Exists(name); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Exists(%q) = %v", name, err)
		}
		if _, err := root.Read(name, 1); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Read(%q) = %v", name, err)
		}
		if err := root.WriteAtomic(name, []byte("value")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("WriteAtomic(%q) = %v", name, err)
		}
	}
	if _, err := root.Read("missing", -1); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Read(negative limit) = %v", err)
	}
	if _, err := root.Read("missing", 1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read(missing) = %v", err)
	}
}

func TestDurableExistsTracksOnlyBoundRegularObjects(t *testing.T) {
	root, path := openTestRoot(t)
	if exists, err := root.Exists("state"); err != nil || exists {
		t.Fatalf("Exists(missing) = %v, %v", exists, err)
	}
	if err := root.WriteAtomic("state", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if exists, err := root.Exists("state"); err != nil || !exists {
		t.Fatalf("Exists(written) = %v, %v", exists, err)
	}
	if err := os.Mkdir(filepath.Join(path, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exists("directory"); err == nil {
		t.Fatal("Exists accepted a directory")
	}
	if err := root.WriteAtomic("directory", []byte("replacement")); err == nil {
		t.Fatal("WriteAtomic replaced a directory")
	}
	if _, err := root.Read("directory", 64); err == nil {
		t.Fatal("Read accepted a directory")
	}
}

func TestDurableRefusesNonDirectoryReplacementAndClosedRoot(t *testing.T) {
	if _, err := OpenRoot("relative"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("OpenRoot(relative) = %v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRoot(file); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("OpenRoot(file) = %v", err)
	}
	if _, err := OpenRoot(filepath.Join(filepath.Dir(file), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenRoot(missing) = %v", err)
	}
	root, path := openTestRoot(t)
	if err := root.WriteAtomic("state", []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Read("state", 64); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("Read(replaced root) = %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exists("state"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Exists(closed) = %v", err)
	}
}

func TestDurableRootRemovalFailsClosedForEveryOperation(t *testing.T) {
	root, path := openTestRoot(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exists("state"); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("Exists after removal = %v", err)
	}
	if _, err := root.Read("state", 64); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("Read after removal = %v", err)
	}
	if err := root.WriteAtomic("state", []byte("value")); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("WriteAtomic after removal = %v", err)
	}
	if _, err := root.Remove("state", sha256.Sum256(nil)); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("Remove after removal = %v", err)
	}
}

func TestDurableRandomFailuresDoNotCommitOrDiscardCandidates(t *testing.T) {
	root, path := openTestRoot(t)
	payload := []byte("value")
	if err := root.WriteAtomic("present", payload); err != nil {
		t.Fatal(err)
	}
	previous := rand.Reader
	rand.Reader = failingRandomReader{}
	t.Cleanup(func() { rand.Reader = previous })
	if err := root.WriteAtomic("state", payload); err == nil {
		t.Fatal("WriteAtomic succeeded without randomness")
	}
	if _, err := root.Remove("present", sha256.Sum256(payload)); err == nil {
		t.Fatal("Remove succeeded without randomness")
	}
	collision := filepath.Join(path, ".state."+strings.Repeat("00", 16)+".tmp")
	if err := os.WriteFile(collision, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	rand.Reader = &zeroThenFailRandomReader{}
	if err := root.WriteAtomic("state", payload); err == nil {
		t.Fatal("WriteAtomic did not retry conflicting temporary name")
	}
	if _, err := os.Stat(collision); err != nil {
		t.Fatalf("WriteAtomic removed existing candidate: %v", err)
	}
}

func TestDurableRemoveRejectsOversizedObject(t *testing.T) {
	root, _ := openTestRoot(t)
	payload := make([]byte, (16<<20)+1)
	if err := root.WriteAtomic("large", payload); err != nil {
		t.Fatal(err)
	}
	if removed, err := root.Remove("large", sha256.Sum256(payload)); removed || !errors.Is(err, ErrReadLimit) {
		t.Fatalf("Remove(large) = %v, %v", removed, err)
	}
}
