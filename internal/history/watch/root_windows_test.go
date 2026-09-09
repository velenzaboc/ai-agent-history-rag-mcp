//go:build windows

package watch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPlatformRootVerifyReturnsDescriptorAndRejectsChangedName(t *testing.T) {
	var unbound platformRoot
	if handle, err := unbound.verify(`C:\missing`); !errors.Is(err, ErrRootUnbound) || handle != windows.InvalidHandle {
		t.Fatalf("unbound verify = %v, %v", handle, err)
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

	handle, err := root.verify(path)
	if err != nil || handle != windows.Handle(root.file.Fd()) {
		t.Fatalf("bound verify = %v, %v", handle, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if handle, err := root.verify(path); !errors.Is(err, ErrRootChanged) || handle != windows.InvalidHandle {
		t.Fatalf("changed-name verify = %v, %v", handle, err)
	}
}
