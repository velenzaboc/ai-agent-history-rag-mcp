//go:build darwin || linux

package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSystemOpsSnapshotsCurrentProcessAndRejectsMissingPID(t *testing.T) {
	ops := NewSystemOps()
	snapshot, err := ops.Snapshot(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PID != os.Getpid() || !filepath.IsAbs(snapshot.Executable) || !filepath.IsAbs(snapshot.CheckoutRoot) || snapshot.StartIdentity == "" {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
	if _, err := ops.Snapshot(999999999); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("Snapshot(missing) error = %v", err)
	}
}

func TestCurrentRecordBindsResolvedCurrentProcessIdentity(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	record, err := CurrentRecord(NewSystemOps(), executable, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if record.PID != os.Getpid() || record.Executable == "" || record.CheckoutRoot == "" || record.StartIdentity == "" {
		t.Fatalf("CurrentRecord() = %#v", record)
	}
	if _, err := CurrentRecord(NewSystemOps(), executable+"-missing", checkout); !errors.Is(err, ErrUncertainTarget) {
		t.Fatalf("CurrentRecord(mismatched executable) error = %v", err)
	}
}

func TestSystemOpsSignalMissingProcessFailsWithoutTargetingAnotherProcess(t *testing.T) {
	err := NewSystemOps().Signal(999999999, os.Interrupt)
	if err == nil {
		t.Fatal("Signal accepted a process that does not exist")
	}
}

func TestDarwinSnapshotParsesOnlyTrustedProcessMetadata(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	resolvedCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	original := commandOutput
	t.Cleanup(func() { commandOutput = original })
	commandOutput = func(name string, arguments ...string) ([]byte, error) {
		switch name {
		case "ps":
			return []byte(fmt.Sprintf("Mon Jan  2 03:04:05 2006 %s\n", executable)), nil
		case "lsof":
			return []byte("p1\nfcwd\nn" + resolvedCheckout + "\n"), nil
		default:
			return nil, fmt.Errorf("unexpected command %q", name)
		}
	}
	snapshot, err := systemSnapshotForOS("darwin", 123)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PID != 123 || snapshot.Executable == "" || snapshot.CheckoutRoot != resolvedCheckout || snapshot.StartIdentity != "Mon Jan 2 03:04:05 2006" {
		t.Fatalf("darwinSnapshotFromCommands() = %#v", snapshot)
	}
	commandOutput = func(string, ...string) ([]byte, error) { return nil, errors.New("unavailable") }
	if _, err := systemSnapshotForOS("darwin", 123); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("systemSnapshotForOS(darwin, ps unavailable) = %v", err)
	}
	for name, output := range map[string]func(string) ([]byte, error){
		"short ps output": func(command string) ([]byte, error) {
			return []byte("too short\n"), nil
		},
		"relative executable": func(command string) ([]byte, error) {
			return []byte("Mon Jan 2 03:04:05 2006 relative-command\n"), nil
		},
		"lsof unavailable": func(command string) ([]byte, error) {
			if command == "ps" {
				return []byte(fmt.Sprintf("Mon Jan  2 03:04:05 2006 %s\n", executable)), nil
			}
			return nil, errors.New("unavailable")
		},
		"lsof missing cwd": func(command string) ([]byte, error) {
			if command == "ps" {
				return []byte(fmt.Sprintf("Mon Jan  2 03:04:05 2006 %s\n", executable)), nil
			}
			return []byte("p1\nfcwd\n"), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			commandOutput = func(command string, _ ...string) ([]byte, error) { return output(command) }
			if _, err := systemSnapshotForOS("darwin", 123); !errors.Is(err, ErrUncertainTarget) {
				t.Fatalf("systemSnapshotForOS(darwin, %s) = %v", name, err)
			}
		})
	}
	if _, err := systemSnapshotForOS("unsupported", 123); !errors.Is(err, ErrUncertainTarget) {
		t.Fatalf("systemSnapshotForOS(unsupported) = %v", err)
	}
}

func TestLinuxSnapshotFromProcRequiresCompleteIdentity(t *testing.T) {
	procRoot := t.TempDir()
	pid := 123
	base := filepath.Join(procRoot, strconv.Itoa(pid))
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := linuxSnapshotFromProc(procRoot, pid); err == nil {
		t.Fatal("snapshot without executable identity succeeded")
	}
	executable := filepath.Join(t.TempDir(), "history-ragd")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(base, "exe")); err != nil {
		t.Fatal(err)
	}
	if _, err := linuxSnapshotFromProc(procRoot, pid); err == nil {
		t.Fatal("snapshot without checkout identity succeeded")
	}
	checkout := t.TempDir()
	if err := os.Symlink(checkout, filepath.Join(base, "cwd")); err != nil {
		t.Fatal(err)
	}
	if _, err := linuxSnapshotFromProc(procRoot, pid); err == nil {
		t.Fatal("snapshot without start identity succeeded")
	}
	if err := os.WriteFile(filepath.Join(base, "stat"), []byte("123 (history-ragd"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := linuxSnapshotFromProc(procRoot, pid); !errors.Is(err, ErrUncertainTarget) {
		t.Fatalf("snapshot with unterminated command = %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "stat"), []byte("123 (history-ragd) S 1 2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := linuxSnapshotFromProc(procRoot, pid); !errors.Is(err, ErrUncertainTarget) {
		t.Fatalf("snapshot with short stat record = %v", err)
	}
}

func TestLinuxSnapshotFromProcReturnsResolvedStableIdentity(t *testing.T) {
	procRoot := t.TempDir()
	pid := 456
	base := filepath.Join(procRoot, strconv.Itoa(pid))
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "history-ragd")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	for link, target := range map[string]string{"exe": executable, "cwd": checkout} {
		if err := os.Symlink(target, filepath.Join(base, link)); err != nil {
			t.Fatal(err)
		}
	}
	stat := "456 (history ragd) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 start-identity"
	if err := os.WriteFile(filepath.Join(base, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := linuxSnapshotFromProc(procRoot, pid)
	if err != nil {
		t.Fatal(err)
	}
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	resolvedCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != (Snapshot{PID: pid, Executable: resolvedExecutable, CheckoutRoot: resolvedCheckout, StartIdentity: "start-identity"}) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
