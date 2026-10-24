//go:build darwin || linux

package watch

import (
	"os"

	"golang.org/x/sys/unix"
)

// forwardSourceIdentity resolves source metadata relative to the held root.
// It never reads source bytes and retains the same no-link and identity checks
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
	rootDescriptor, err := r.platform.verify(r.path)
	if err != nil {
		return RootIdentity{}, err
	}
	parent := rootDescriptor
	var openedDirectories []*os.File
	defer func() {
		for index := len(openedDirectories) - 1; index >= 0; index-- {
			_ = openedDirectories[index].Close()
		}
	}()
	for _, component := range parts[:len(parts)-1] {
		before, statErr := unixStatAt(parent, component)
		if statErr != nil || validateUnixDirectory(before) != nil {
			return RootIdentity{}, ErrUnsafeSource
		}
		descriptor, openErr := unix.Openat(parent, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return RootIdentity{}, ErrUnsafeSource
		}
		opened := os.NewFile(uintptr(descriptor), component)
		if opened == nil {
			_ = unix.Close(descriptor)
			return RootIdentity{}, ErrUnsafeSource
		}
		var held unix.Stat_t
		current, currentErr := unixStatAt(parent, component)
		if unix.Fstat(descriptor, &held) != nil || currentErr != nil || validateUnixDirectory(held) != nil || !sameUnixIdentity(before, held) || !sameUnixIdentity(held, current) {
			_ = opened.Close()
			return RootIdentity{}, ErrUnsafeSource
		}
		openedDirectories = append(openedDirectories, opened)
		parent = descriptor
	}

	name := parts[len(parts)-1]
	before, err := unixStatAt(parent, name)
	if err != nil || validateUnixFile(before) != nil {
		return RootIdentity{}, ErrUnsafeSource
	}
	descriptor, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return RootIdentity{}, ErrUnsafeSource
	}
	source := os.NewFile(uintptr(descriptor), name)
	if source == nil {
		_ = unix.Close(descriptor)
		return RootIdentity{}, ErrUnsafeSource
	}
	defer source.Close()
	var held unix.Stat_t
	current, currentErr := unixStatAt(parent, name)
	if unix.Fstat(descriptor, &held) != nil || currentErr != nil || validateUnixFile(held) != nil || !sameUnixIdentity(before, held) || !sameUnixIdentity(held, current) {
		return RootIdentity{}, ErrUnsafeSource
	}
	if _, err := r.platform.verify(r.path); err != nil {
		return RootIdentity{}, err
	}
	return RootIdentity{Volume: uint64(held.Dev), Object: uint64(held.Ino)}, nil
}
