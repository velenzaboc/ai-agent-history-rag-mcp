//go:build darwin || linux

package durable

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUnixDurableRequiresOwnerOnlyModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRoot(path); !errors.Is(err, ErrNotOwnerOnly) {
		t.Fatalf("OpenRoot(group-readable) = %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := root.WriteAtomic("state", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(path, "state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Read("state", 64); !errors.Is(err, ErrNotOwnerOnly) {
		t.Fatalf("Read(public file) = %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exists("state"); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("Exists(public root) = %v", err)
	}
}

func TestUnixDurableRefusesLinks(t *testing.T) {
	root, path := openTestRoot(t)
	rootTarget := filepath.Join(t.TempDir(), "root-target")
	if err := os.Mkdir(rootTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(rootTarget, rootLink); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRoot(rootLink); err == nil {
		t.Fatal("OpenRoot followed a symbolic link")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Read("link", 64); err == nil {
		t.Fatal("Read followed symlink")
	}
	if err := os.Link(outside, filepath.Join(path, "hard")); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Read("hard", 64); err == nil {
		t.Fatal("Read accepted hard link")
	}
}

func TestUnixDurableInternalReadersFailClosed(t *testing.T) {
	root, path := openTestRoot(t)
	fd, err := root.verify()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.readLockedWithoutMutex(fd, "missing", 1); err == nil {
		t.Fatal("internal reader accepted missing object")
	}
	if err := os.WriteFile(filepath.Join(path, "public"), []byte("value"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := root.readLockedWithoutMutex(fd, "public", 64); !errors.Is(err, ErrNotOwnerOnly) {
		t.Fatalf("internal reader accepted public object: %v", err)
	}
	broken := &Root{path: path, file: os.NewFile(^uintptr(0), "broken")}
	if _, err := broken.verify(); err == nil {
		t.Fatal("verify accepted a broken descriptor")
	}
	if _, err := broken.readLockedWithoutMutex(-1, "public", 64); err == nil {
		t.Fatal("internal reader accepted a broken descriptor")
	}
}

func TestUnixDurableSyscallFailuresRemainFailClosed(t *testing.T) {
	t.Run("read open failure", func(t *testing.T) {
		root, _ := openTestRoot(t)
		if err := root.WriteAtomic("state", []byte("value")); err != nil {
			t.Fatal(err)
		}
		previous := openAt
		openAt = func(int, string, int, uint32) (int, error) { return -1, unix.EACCES }
		t.Cleanup(func() { openAt = previous })
		if _, err := root.Read("state", 64); !errors.Is(err, unix.EACCES) {
			t.Fatalf("Read denied open = %v", err)
		}
	})
	t.Run("write failure", func(t *testing.T) {
		root, _ := openTestRoot(t)
		previous := openAt
		openAt = func(int, string, int, uint32) (int, error) { return -1, unix.EIO }
		t.Cleanup(func() { openAt = previous })
		if err := root.WriteAtomic("state", []byte("value")); !errors.Is(err, unix.EIO) {
			t.Fatalf("WriteAtomic failed temporary creation = %v", err)
		}
	})
	t.Run("write chmod and commit-sync failures", func(t *testing.T) {
		root, path := openTestRoot(t)
		previousChmod := fchmod
		fchmod = func(int, uint32) error { return unix.EIO }
		t.Cleanup(func() { fchmod = previousChmod })
		if err := root.WriteAtomic("state", []byte("value")); !errors.Is(err, unix.EIO) {
			t.Fatalf("WriteAtomic chmod failure = %v", err)
		}
		if _, err := os.Stat(filepath.Join(path, "state")); !os.IsNotExist(err) {
			t.Fatalf("failed chmod left target: %v", err)
		}
	})
	t.Run("write directory-sync failure retains committed target", func(t *testing.T) {
		root, path := openTestRoot(t)
		previousSync := fsync
		fsync = func(int) error { return unix.EIO }
		t.Cleanup(func() { fsync = previousSync })
		if err := root.WriteAtomic("state", []byte("value")); !errors.Is(err, unix.EIO) {
			t.Fatalf("WriteAtomic sync failure = %v", err)
		}
		if _, err := os.Stat(filepath.Join(path, "state")); err != nil {
			t.Fatalf("committed target missing after uncertain sync: %v", err)
		}
	})
	t.Run("read fails closed when the opened descriptor is invalid", func(t *testing.T) {
		root, _ := openTestRoot(t)
		if err := root.WriteAtomic("state", []byte("value")); err != nil {
			t.Fatal(err)
		}
		previousOpen := openAt
		openAt = func(int, string, int, uint32) (int, error) { return -1, nil }
		t.Cleanup(func() { openAt = previousOpen })
		if _, err := root.Read("state", 64); err == nil {
			t.Fatal("Read accepted an invalid opened descriptor")
		}
	})
	t.Run("read fails closed when the named object changes after open", func(t *testing.T) {
		root, path := openTestRoot(t)
		if err := root.WriteAtomic("state", []byte("original")); err != nil {
			t.Fatal(err)
		}
		previousOpen := openAt
		openAt = func(fd int, name string, flags int, mode uint32) (int, error) {
			opened, err := previousOpen(fd, name, flags, mode)
			if err != nil {
				return opened, err
			}
			if err := os.Remove(filepath.Join(path, name)); err != nil {
				return -1, err
			}
			if err := os.WriteFile(filepath.Join(path, name), []byte("replacement"), 0o600); err != nil {
				return -1, err
			}
			return opened, nil
		}
		t.Cleanup(func() { openAt = previousOpen })
		if _, err := root.Read("state", 64); err == nil {
			t.Fatal("Read accepted an object replaced after open")
		}
	})
	t.Run("remove rename failure", func(t *testing.T) {
		root, _ := openTestRoot(t)
		payload := []byte("value")
		if err := root.WriteAtomic("state", payload); err != nil {
			t.Fatal(err)
		}
		previous := renameAt
		renameAt = func(int, string, int, string) error { return unix.EIO }
		t.Cleanup(func() { renameAt = previous })
		if _, err := root.Remove("state", sha256.Sum256(payload)); !errors.Is(err, unix.EIO) {
			t.Fatalf("Remove failed rename = %v", err)
		}
	})
	t.Run("remove commit and cleanup sync failures", func(t *testing.T) {
		root, _ := openTestRoot(t)
		payload := []byte("value")
		if err := root.WriteAtomic("state", payload); err != nil {
			t.Fatal(err)
		}
		previousSync := fsync
		fsync = func(int) error { return unix.EIO }
		t.Cleanup(func() { fsync = previousSync })
		if _, err := root.Remove("state", sha256.Sum256(payload)); !errors.Is(err, unix.EIO) {
			t.Fatalf("Remove commit sync failure = %v", err)
		}
	})
	t.Run("remove cleanup failure", func(t *testing.T) {
		root, _ := openTestRoot(t)
		payload := []byte("value")
		if err := root.WriteAtomic("state", payload); err != nil {
			t.Fatal(err)
		}
		previousUnlink := unlinkAt
		unlinkAt = func(int, string, int) error { return unix.EIO }
		t.Cleanup(func() { unlinkAt = previousUnlink })
		if _, err := root.Remove("state", sha256.Sum256(payload)); !errors.Is(err, unix.EIO) {
			t.Fatalf("Remove cleanup failure = %v", err)
		}
	})
	t.Run("remove cleanup sync failure", func(t *testing.T) {
		root, _ := openTestRoot(t)
		payload := []byte("value")
		if err := root.WriteAtomic("state", payload); err != nil {
			t.Fatal(err)
		}
		previousSync := fsync
		calls := 0
		fsync = func(fd int) error {
			calls++
			if calls == 2 {
				return unix.EIO
			}
			return previousSync(fd)
		}
		t.Cleanup(func() { fsync = previousSync })
		if _, err := root.Remove("state", sha256.Sum256(payload)); !errors.Is(err, unix.EIO) {
			t.Fatalf("Remove cleanup sync failure = %v", err)
		}
	})
}
