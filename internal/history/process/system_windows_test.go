//go:build windows

package process

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsSystemOpsBindsOnlyTheCurrentResolvedProcess(t *testing.T) {
	ops := NewSystemOps()
	if _, err := ops.Snapshot(os.Getpid()); !errors.Is(err, ErrUncertainTarget) {
		t.Fatalf("unbound Snapshot() error = %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	record, err := CurrentRecord(ops, executable, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if record.PID != os.Getpid() || !filepath.IsAbs(record.Executable) || !filepath.IsAbs(record.CheckoutRoot) || record.StartIdentity == "" {
		t.Fatalf("CurrentRecord() = %#v", record)
	}
	if _, err := ops.Snapshot(999999999); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("Snapshot(missing) error = %v", err)
	}
	if err := ops.Signal(os.Getpid(), os.Interrupt); !errors.Is(err, ErrUncertainTarget) {
		t.Fatalf("Signal(non-kill) error = %v", err)
	}
	if err := ops.Signal(999999999, os.Kill); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("Signal(missing process) error = %v", err)
	}

	ops.mu.Lock()
	ops.expectedExecutable = filepath.Join(record.CheckoutRoot, "not-the-current-process.exe")
	ops.mu.Unlock()
	if _, err := ops.Snapshot(os.Getpid()); !errors.Is(err, ErrUncertainTarget) {
		t.Fatalf("Snapshot(mismatched executable) error = %v", err)
	}
}

func TestWindowsCurrentRecordRejectsUnprovenCoordinates(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CurrentRecord(nil, executable, checkout); !errors.Is(err, ErrUncertainTarget) {
		t.Fatalf("CurrentRecord(nil ops) error = %v", err)
	}
	if _, err := CurrentRecord(NewSystemOps(), executable+".missing", checkout); !errors.Is(err, ErrUncertainTarget) {
		t.Fatalf("CurrentRecord(missing executable) error = %v", err)
	}
	if _, err := CurrentRecord(NewSystemOps(), executable, checkout+".missing"); !errors.Is(err, ErrUncertainTarget) {
		t.Fatalf("CurrentRecord(missing checkout) error = %v", err)
	}
}
