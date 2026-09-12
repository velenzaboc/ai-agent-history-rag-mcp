// Command go-test-integrity checks Go test bodies with the Go parser rather
// than by scanning source text. Comments and string literals are intentionally
// absent from the AST, so they cannot impersonate an assertion.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type finding struct {
	file string
	line int
	name string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("go-test-integrity", flag.ContinueOnError)
	flags.SetOutput(stderr)
	filesPath := flags.String("files", "", "newline-delimited Go test file paths")
	if err := flags.Parse(args); err != nil || *filesPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: go-test-integrity --files PATH")
		return 2
	}
	files, err := readFiles(*filesPath)
	if err != nil {
		fmt.Fprintf(stderr, "go-test-integrity: %v\n", err)
		return 2
	}
	for _, path := range files {
		found, err := inspectFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "go-test-integrity: %v\n", err)
			return 2
		}
		for _, item := range found {
			fmt.Fprintf(stdout, "ASSERTION-FREE TEST  %s:%d: %s has neither a testing.T reference nor a panic failure path\n", item.file, item.line, item.name)
		}
		if len(found) != 0 {
			return 1
		}
	}
	return 0
}

func readFiles(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open test-file enumeration %q: %w", path, err)
	}
	defer f.Close()

	var files []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if value := strings.TrimSpace(scanner.Text()); value != "" {
			files = append(files, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read test-file enumeration %q: %w", path, err)
	}
	return files, nil
}

func inspectFile(path string) ([]finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	return inspectAST(fset, file), nil
}

func inspectAST(fset *token.FileSet, file *ast.File) []finding {
	info := typeInfo(fset, file)
	var findings []finding
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !isTestName(fn.Name.Name) {
			continue
		}
		param, ok := testingTParam(fn)
		paramObject := info.Defs[param]
		if !ok || paramObject == nil || bodyCanFail(fn.Body, paramObject, info) {
			continue
		}
		findings = append(findings, finding{
			file: filepath.Clean(fset.Position(fn.Pos()).Filename),
			line: fset.Position(fn.Pos()).Line,
			name: fn.Name.Name,
		})
	}
	return findings
}

func typeInfo(fset *token.FileSet, file *ast.File) *types.Info {
	info := &types.Info{
		Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{},
	}
	config := types.Config{
		Importer: importer.Default(),
		Error:    func(error) {},
	}
	// A test file can reference package-local declarations not present in this
	// one-file inspection. Errors do not erase already resolved local bindings,
	// which are the only facts this integrity check consumes.
	_, _ = config.Check(file.Name.Name, fset, []*ast.File{file}, info)
	return info
}

func isTestName(name string) bool {
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	if len(name) == len("Test") {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len("Test"):])
	return !unicode.IsLower(r)
}

func testingTParam(fn *ast.FuncDecl) (*ast.Ident, bool) {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) != 1 {
		return nil, false
	}
	field := fn.Type.Params.List[0]
	star, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return nil, false
	}
	selector, ok := star.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "T" {
		return nil, false
	}
	return field.Names[0], true
}

func bodyCanFail(body *ast.BlockStmt, param types.Object, info *types.Info) bool {
	canFail := false
	ast.Inspect(body, func(node ast.Node) bool {
		if canFail || node == nil {
			return !canFail
		}
		switch value := node.(type) {
		case *ast.Ident:
			if info.Uses[value] == param {
				canFail = true
			}
		case *ast.CallExpr:
			if fn, ok := value.Fun.(*ast.Ident); ok {
				if builtin, ok := info.Uses[fn].(*types.Builtin); ok && builtin.Name() == "panic" {
					canFail = true
				}
			}
		}
		return true
	})
	return canFail
}
