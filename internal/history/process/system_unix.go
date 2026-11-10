//go:build darwin || linux

package process

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

type SystemOps struct{}

func NewSystemOps() *SystemOps { return &SystemOps{} }

var commandOutput = func(name string, arguments ...string) ([]byte, error) {
	return exec.Command(name, arguments...).Output()
}

func systemSnapshot(pid int) (Snapshot, error) {
	return systemSnapshotForOS(runtime.GOOS, pid)
}

func systemSnapshotForOS(goos string, pid int) (Snapshot, error) {
	switch goos {
	case "linux":
		return linuxSnapshotFromProc("/proc", pid)
	case "darwin":
		return darwinSnapshotFromCommands(pid)
	default:
		return Snapshot{}, ErrUncertainTarget
	}
}

func mapMissingProcessError(err error) error {
	if os.IsNotExist(err) {
		return ErrProcessNotFound
	}
	return err
}

// linuxSnapshotFromProc keeps the procfs identity interpretation separate from
// the live /proc root. The process controller deliberately treats any absent
// identity component as an error; callers must not act on a PID whose complete
// identity cannot be established.
func linuxSnapshotFromProc(procRoot string, pid int) (Snapshot, error) {
	base := filepath.Join(procRoot, strconv.Itoa(pid))
	exePath := filepath.Join(base, "exe")
	executable, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		rawExe, readlinkErr := os.Readlink(exePath)
		if readlinkErr != nil {
			return Snapshot{}, mapMissingProcessError(readlinkErr)
		}
		executable = strings.TrimSuffix(rawExe, " (deleted)")
		if !filepath.IsAbs(executable) {
			return Snapshot{}, ErrUncertainTarget
		}
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
	}
	checkout, err := filepath.EvalSymlinks(filepath.Join(base, "cwd"))
	if err != nil {
		return Snapshot{}, mapMissingProcessError(err)
	}
	stat, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return Snapshot{}, mapMissingProcessError(err)
	}
	closing := strings.LastIndexByte(string(stat), ')')
	if closing < 0 {
		return Snapshot{}, ErrUncertainTarget
	}
	fields := strings.Fields(string(stat[closing+1:]))
	if len(fields) < 20 {
		return Snapshot{}, ErrUncertainTarget
	}
	return Snapshot{PID: pid, Executable: executable, CheckoutRoot: checkout, StartIdentity: fields[19]}, nil
}

func darwinSnapshotFromCommands(pid int) (Snapshot, error) {
	pidArg := strconv.Itoa(pid)
	psOutput, err := commandOutput("ps", "-ww", "-p", pidArg, "-o", "lstart=", "-o", "command=")
	if err != nil {
		psOutput, err = commandOutput("/bin/ps", "-ww", "-p", pidArg, "-o", "lstart=", "-o", "command=")
	}
	if err != nil {
		return Snapshot{}, ErrProcessNotFound
	}
	fields := strings.Fields(string(psOutput))
	if len(fields) < 6 {
		return Snapshot{}, ErrUncertainTarget
	}
	executable := fields[5]
	if !filepath.IsAbs(executable) {
		return Snapshot{}, ErrUncertainTarget
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return Snapshot{}, ErrUncertainTarget
	}
	lsofOutput, err := commandOutput("lsof", "-a", "-p", pidArg, "-d", "cwd,txt", "-Fn")
	if err != nil {
		lsofOutput, err = commandOutput("/usr/sbin/lsof", "-a", "-p", pidArg, "-d", "cwd,txt", "-Fn")
	}
	if err != nil {
		lsofOutput, err = commandOutput("/usr/bin/lsof", "-a", "-p", pidArg, "-d", "cwd,txt", "-Fn")
	}
	if err != nil {
		return Snapshot{}, ErrUncertainTarget
	}
	checkout := ""
	field := "cwd"
	scanner := bufio.NewScanner(strings.NewReader(string(lsofOutput)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "f") {
			field = strings.TrimPrefix(line, "f")
		} else if field == "cwd" && strings.HasPrefix(line, "n/") {
			checkout = strings.TrimPrefix(line, "n")
			break
		}
	}
	if checkout == "" || scanner.Err() != nil {
		return Snapshot{}, ErrUncertainTarget
	}
	checkout, err = filepath.EvalSymlinks(checkout)
	if err != nil {
		return Snapshot{}, ErrUncertainTarget
	}
	return Snapshot{PID: pid, Executable: executable, CheckoutRoot: checkout, StartIdentity: strings.Join(fields[:5], " ")}, nil
}

func (SystemOps) Signal(pid int, signal os.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(signal); errors.Is(err, os.ErrProcessDone) {
		return ErrProcessNotFound
	} else {
		return err
	}
}

func (SystemOps) Snapshot(pid int) (Snapshot, error) {
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return Snapshot{}, ErrProcessNotFound
		}
		return Snapshot{}, err
	}
	return systemSnapshot(pid)
}

func terminationSignal() os.Signal { return syscall.SIGTERM }

func CurrentRecord(ops Ops, executable, checkoutRoot string) (Record, error) {
	snapshot, err := ops.Snapshot(os.Getpid())
	if err != nil {
		return Record{}, fmt.Errorf("inspect current process: %w", err)
	}
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil || resolvedExecutable != snapshot.Executable {
		return Record{}, ErrUncertainTarget
	}
	resolvedCheckout, err := filepath.EvalSymlinks(checkoutRoot)
	if err != nil || resolvedCheckout != snapshot.CheckoutRoot {
		return Record{}, ErrUncertainTarget
	}
	return Record(snapshot), nil
}
