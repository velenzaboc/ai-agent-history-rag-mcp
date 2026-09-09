//go:build windows

package gcpauth

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func currentTestSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return "*" + user.User.Sid.String()
}

func runICACLS(t *testing.T, path string, args ...string) {
	t.Helper()
	commandArgs := append([]string{path}, args...)
	if output, err := exec.Command("icacls.exe", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("icacls %v: %v (%s)", commandArgs, err, output)
	}
}

func restrictTestFileAccess(t *testing.T, path string) {
	t.Helper()
	runICACLS(t, path, "/inheritance:r", "/grant:r", currentTestSID(t)+":(F)")
}

func loosenTestFileAccess(t *testing.T, path string) {
	t.Helper()
	restrictTestFileAccess(t, path)
	// Builtin Users is intentionally added only for the negative ACL test.
	runICACLS(t, path, "/grant", "*S-1-5-32-545:(R)")
}
