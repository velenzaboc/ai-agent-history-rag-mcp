//go:build windows

package watch

import (
	"os"

	"golang.org/x/sys/windows"
)

// forwardSourceIdentity resolves source metadata relative to the held root.
// It never reads source bytes and retains the same reparse and identity checks
// as a content snapshot.
func (r *PinnedRoot) forwardSourceIdentity(candidate string) (RootIdentity, error) {
	parts, err := relativeParts(r.path, candidate)
	if err != nil {
		return RootIdentity{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return RootIdentity{}, os.ErrClosed
	}
	rootHandle, err := r.platform.verify(r.path)
	if err != nil {
		return RootIdentity{}, err
	}
	parent := rootHandle
	var openedDirectories []*os.File
	defer func() {
		for index := len(openedDirectories) - 1; index >= 0; index-- {
			_ = openedDirectories[index].Close()
		}
	}()
	for _, component := range parts[:len(parts)-1] {
		opened, identity, err := openWindowsRelative(parent, component, true)
		if err != nil {
			return RootIdentity{}, ErrUnsafeSource
		}
		if err := reopenWindowsRelative(parent, component, true, identity); err != nil {
			_ = opened.Close()
			return RootIdentity{}, err
		}
		openedDirectories = append(openedDirectories, opened)
		parent = windows.Handle(opened.Fd())
	}

	name := parts[len(parts)-1]
	source, identity, err := openWindowsRelative(parent, name, false)
	if err != nil {
		return RootIdentity{}, ErrUnsafeSource
	}
	defer source.Close()
	if err := reopenWindowsRelative(parent, name, false, identity); err != nil {
		return RootIdentity{}, err
	}
	if _, err := r.platform.verify(r.path); err != nil {
		return RootIdentity{}, err
	}
	return RootIdentity{
		Volume: uint64(identity.VolumeSerialNumber),
		Object: uint64(identity.FileIndexHigh)<<32 | uint64(identity.FileIndexLow),
	}, nil
}
