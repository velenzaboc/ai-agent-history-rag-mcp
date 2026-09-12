package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectASTUsesGoSyntaxNotRawText(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   int
	}{
		{"comment", "package p\nimport \"testing\"\nfunc TestComment(t *testing.T) { // t\n }", 1},
		{"quoted", "package p\nimport \"testing\"\nfunc TestQuoted(t *testing.T) { _ = \"t\" }", 1},
		{"raw brace", "package p\nimport \"testing\"\nfunc TestRaw(t *testing.T) { _ = `}\nt`; if true { t.Fatal(\"x\") } }", 0},
		{"spaced", "package p\nimport \"testing\"\nfunc TestSpaced ( t * testing.T ) {}", 1},
		{"nested and helper", "package p\nimport \"testing\"\nfunc TestNested(t *testing.T) { if true { helper(t) } }\nfunc helper(t *testing.T) {}", 0},
		{"panic", "package p\nimport \"testing\"\nfunc TestPanic(t *testing.T) { if false { panic(\"failure\") } }", 0},
		{"shadowed testing parameter", "package p\nimport \"testing\"\nfunc TestShadow(t *testing.T) { if true { t := struct{}{}; _ = t } }", 1},
		{"shadowed panic", "package p\nimport \"testing\"\nfunc TestShadowPanic(t *testing.T) { panic := func(string) {}; panic(\"not builtin\") }", 1},
		{"digit test suffix", "package p\nimport \"testing\"\nfunc Test1(t *testing.T) {}", 1},
		{"underscore test suffix", "package p\nimport \"testing\"\nfunc Test_(t *testing.T) {}", 1},
		{"lowercase suffix is not a Go test", "package p\nimport \"testing\"\nfunc Testlower(t *testing.T) {}", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, test.name+".go", test.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(inspectAST(fset, file)); got != test.want {
				t.Fatalf("findings=%d, want %d", got, test.want)
			}
		})
	}
}

func TestRunReportsFindingsAndInputFailures(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good_test.go")
	hollow := filepath.Join(dir, "hollow_test.go")
	if err := os.WriteFile(good, []byte("package p\nimport \"testing\"\nfunc TestGood(t *testing.T) { t.Fatal(\"x\") }"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hollow, []byte("package p\nimport \"testing\"\nfunc TestHollow(t *testing.T) {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "files.txt")
	if err := os.WriteFile(list, []byte(good+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"--files", list}, &stdout, &stderr); got != 0 {
		t.Fatalf("good run exit=%d stderr=%s", got, stderr.String())
	}
	if err := os.WriteFile(list, []byte(hollow+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"--files", list}, &stdout, &stderr); got != 1 {
		t.Fatalf("hollow run exit=%d stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "TestHollow") {
		t.Fatalf("missing finding: %s", stdout.String())
	}
	if got := run(nil, &stdout, &stderr); got != 2 {
		t.Fatalf("empty args exit=%d", got)
	}
	if got := run([]string{"--files", filepath.Join(dir, "missing.txt")}, &stdout, &stderr); got != 2 {
		t.Fatalf("missing list exit=%d", got)
	}
	if err := os.WriteFile(list, []byte(filepath.Join(dir, "broken_test.go")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken_test.go"), []byte("package p\nfunc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := run([]string{"--files", list}, &stdout, &stderr); got != 2 {
		t.Fatalf("parse error exit=%d", got)
	}
}
