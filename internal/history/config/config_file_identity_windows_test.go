//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type syntheticFileInfo struct{ mode os.FileMode }

func (s syntheticFileInfo) Name() string       { return "state" }
func (s syntheticFileInfo) Size() int64        { return 0 }
func (s syntheticFileInfo) Mode() os.FileMode  { return s.mode }
func (s syntheticFileInfo) ModTime() time.Time { return time.Time{} }
func (s syntheticFileInfo) IsDir() bool        { return true }
func (s syntheticFileInfo) Sys() any           { return nil }

func TestOwnerOnlyStateDirectoryModeDoesNotUseSynthesizedWindowsBits(t *testing.T) {
	for _, mode := range []os.FileMode{os.ModeDir | 0o777, os.ModeDir | 0o555} {
		if err := requireOwnerOnlyStateDirectoryMode(syntheticFileInfo{mode: mode}); err != nil {
			t.Fatalf("synthesized mode %v rejected: %v", mode, err)
		}
	}
}

func TestWindowsRequireSingleLinkAcceptsPrivateRegularFileAndRejectsMissingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireSingleLink(path); err != nil {
		t.Fatalf("requireSingleLink(private regular file) = %v", err)
	}
	if err := requireSingleLink(path + ".missing"); err == nil {
		t.Fatal("requireSingleLink accepted a missing path")
	}
}
