package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func newForwardIdentityRoot(t *testing.T) (*PinnedRoot, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := NewPinnedRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	bound, err := root.Bind()
	if err != nil || !bound {
		t.Fatalf("Bind() = %t, %v", bound, err)
	}
	return root, path
}

func TestForwardProducerBindsEligibilityToSnapshottedObject(t *testing.T) {
	const legacySession = "legacy-session-alpha"
	sourceRoot := t.TempDir()
	legacyPath := filepath.Join(sourceRoot, "legacy-session.jsonl")
	eligiblePath := filepath.Join(sourceRoot, "new-session.jsonl")
	savedNewPath := filepath.Join(sourceRoot, "new-session.saved")
	future := time.Now().UTC().Add(24 * time.Hour)

	// The constructor is the sole source of exclusion authority. Although this
	// baseline content looks current, its file object existed before enablement.
	writeForwardFile(t, legacyPath, forwardTurn(legacySession, "baseline", future))
	legacyInfo, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	registry := Registry{MachineID: "identity-race", MaxBytes: MaxSourceSnapshotBytes}
	producer, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })

	writeForwardFile(t, eligiblePath, forwardTurn("new-session-alpha", "eligible", future.Add(time.Hour)))
	realSnapshot := snapshotForwardSource
	swapped := false
	snapshotForwardSource = func(root *PinnedRoot, path string, maxBytes int64) (*Snapshot, error) {
		if filepath.Clean(path) == filepath.Clean(eligiblePath) && !swapped {
			swapped = true
			if err := os.Rename(eligiblePath, savedNewPath); err != nil {
				return nil, err
			}
			if err := os.Rename(legacyPath, eligiblePath); err != nil {
				return nil, err
			}
		}
		return root.Snapshot(path, maxBytes)
	}
	t.Cleanup(func() { snapshotForwardSource = realSnapshot })

	scanErr := producer.Scan(context.Background())
	if scanErr != nil && !errors.Is(scanErr, ErrSourceChanged) {
		t.Fatalf("raced Scan() error = %v, want nil or %v", scanErr, ErrSourceChanged)
	}
	if !swapped {
		t.Fatal("snapshot seam did not replace the eligible path")
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("baseline object raced into eligible path and uploaded: %d batches", len(sink.chunks))
	}
	racedInfo, err := os.Stat(eligiblePath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(legacyInfo, racedInfo) {
		t.Fatal("race fixture did not place the baseline file object at the eligible path")
	}

	postRaceSnapshots := 0
	snapshotForwardSource = func(root *PinnedRoot, path string, maxBytes int64) (*Snapshot, error) {
		if filepath.Clean(path) == filepath.Clean(eligiblePath) {
			postRaceSnapshots++
		}
		return root.Snapshot(path, maxBytes)
	}
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() after object race = %v", err)
	}
	if postRaceSnapshots != 0 || len(sink.chunks) != 0 {
		t.Fatalf("baseline object was not excluded after race: snapshots=%d batches=%d", postRaceSnapshots, len(sink.chunks))
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatalf("restart after object race: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Scan(context.Background()); err != nil {
		t.Fatalf("restart Scan() after object race = %v", err)
	}
	if postRaceSnapshots != 0 || len(sink.chunks) != 0 {
		t.Fatalf("restart forgot baseline object exclusion: snapshots=%d batches=%d", postRaceSnapshots, len(sink.chunks))
	}
}

func TestForwardSourceIdentityAcceptsStableNestedFileObject(t *testing.T) {
	root, rootPath := newForwardIdentityRoot(t)
	parent := filepath.Join(rootPath, "project", "sessions")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "conversation.jsonl")
	writeForwardFile(t, path, []byte("metadata identity does not read content\n"))

	first, err := root.forwardSourceIdentity(path)
	if err != nil {
		t.Fatalf("forwardSourceIdentity(valid) = %v", err)
	}
	if first == (RootIdentity{}) {
		t.Fatal("valid source returned an empty identity")
	}
	second, err := root.forwardSourceIdentity(path)
	if err != nil || second != first {
		t.Fatalf("repeat identity = %#v, %v; want %#v", second, err, first)
	}

	moved := filepath.Join(parent, "renamed.jsonl")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	renamed, err := root.forwardSourceIdentity(moved)
	if err != nil || renamed != first {
		t.Fatalf("renamed identity = %#v, %v; want %#v", renamed, err, first)
	}
}

func TestForwardSourceIdentityRefusesClosedAndUnboundRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := NewPinnedRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(path, "conversation.jsonl")
	writeForwardFile(t, candidate, []byte("content must not be inspected\n"))

	if _, err := root.forwardSourceIdentity(candidate); !errors.Is(err, ErrRootUnbound) {
		t.Fatalf("unbound identity error = %v, want %v", err, ErrRootUnbound)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.forwardSourceIdentity(candidate); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed identity error = %v, want %v", err, os.ErrClosed)
	}
}

func TestForwardSourceIdentityRefusesOutsideAndUncleanCoordinates(t *testing.T) {
	root, rootPath := newForwardIdentityRoot(t)
	separator := string(filepath.Separator)
	cases := map[string]string{
		"relative":        filepath.Join("relative", "conversation.jsonl"),
		"root itself":     rootPath,
		"outside sibling": filepath.Join(filepath.Dir(rootPath), "outside.jsonl"),
		"unclean child":   rootPath + separator + "child" + separator + ".." + separator + "conversation.jsonl",
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := root.forwardSourceIdentity(candidate); !errors.Is(err, ErrOutsideRoot) {
				t.Fatalf("forwardSourceIdentity(%q) error = %v, want %v", candidate, err, ErrOutsideRoot)
			}
		})
	}
}

func TestForwardSourceIdentityRefusesUnsafeSourceShapes(t *testing.T) {
	root, rootPath := newForwardIdentityRoot(t)

	t.Run("missing leaf", func(t *testing.T) {
		if _, err := root.forwardSourceIdentity(filepath.Join(rootPath, "missing.jsonl")); !errors.Is(err, ErrUnsafeSource) {
			t.Fatalf("missing identity error = %v, want %v", err, ErrUnsafeSource)
		}
	})

	t.Run("missing parent", func(t *testing.T) {
		candidate := filepath.Join(rootPath, "missing", "conversation.jsonl")
		if _, err := root.forwardSourceIdentity(candidate); !errors.Is(err, ErrUnsafeSource) {
			t.Fatalf("missing parent identity error = %v, want %v", err, ErrUnsafeSource)
		}
	})

	t.Run("directory as source", func(t *testing.T) {
		path := filepath.Join(rootPath, "directory.jsonl")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := root.forwardSourceIdentity(path); !errors.Is(err, ErrUnsafeSource) {
			t.Fatalf("directory identity error = %v, want %v", err, ErrUnsafeSource)
		}
	})

	t.Run("regular file as parent", func(t *testing.T) {
		parent := filepath.Join(rootPath, "not-a-directory")
		writeForwardFile(t, parent, []byte("not a directory\n"))
		candidate := filepath.Join(parent, "conversation.jsonl")
		if _, err := root.forwardSourceIdentity(candidate); !errors.Is(err, ErrUnsafeSource) {
			t.Fatalf("non-directory parent identity error = %v, want %v", err, ErrUnsafeSource)
		}
	})

	t.Run("hard-linked source", func(t *testing.T) {
		target := filepath.Join(rootPath, "hard-link-target.jsonl")
		writeForwardFile(t, target, []byte("hard-linked source\n"))
		linked := filepath.Join(rootPath, "hard-linked.jsonl")
		if err := os.Link(target, linked); err != nil {
			t.Fatal(err)
		}
		if _, err := root.forwardSourceIdentity(linked); !errors.Is(err, ErrUnsafeSource) {
			t.Fatalf("hard-linked source identity error = %v, want %v", err, ErrUnsafeSource)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("linked parent", func(t *testing.T) {
			target := filepath.Join(rootPath, "target-parent")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			writeForwardFile(t, filepath.Join(target, "conversation.jsonl"), []byte("linked parent\n"))
			linked := filepath.Join(rootPath, "linked-parent")
			if err := os.Symlink(target, linked); err != nil {
				t.Fatal(err)
			}
			if _, err := root.forwardSourceIdentity(filepath.Join(linked, "conversation.jsonl")); !errors.Is(err, ErrUnsafeSource) {
				t.Fatalf("linked parent identity error = %v, want %v", err, ErrUnsafeSource)
			}
		})

		t.Run("linked source", func(t *testing.T) {
			target := filepath.Join(rootPath, "target.jsonl")
			writeForwardFile(t, target, []byte("linked source\n"))
			linked := filepath.Join(rootPath, "linked.jsonl")
			if err := os.Symlink(target, linked); err != nil {
				t.Fatal(err)
			}
			if _, err := root.forwardSourceIdentity(linked); !errors.Is(err, ErrUnsafeSource) {
				t.Fatalf("linked source identity error = %v, want %v", err, ErrUnsafeSource)
			}
		})
	}
}

func TestForwardSourceIdentityRefusesReplacedRoot(t *testing.T) {
	root, rootPath := newForwardIdentityRoot(t)
	movedRoot := rootPath + "-moved"
	if err := os.Rename(rootPath, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(rootPath, "conversation.jsonl")
	writeForwardFile(t, candidate, []byte("replacement root\n"))
	if _, err := root.forwardSourceIdentity(candidate); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("replaced root identity error = %v, want %v", err, ErrRootChanged)
	}
}
