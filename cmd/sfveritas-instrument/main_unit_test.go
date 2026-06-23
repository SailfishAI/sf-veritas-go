package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCompileArgs(t *testing.T) {
	args := []string{
		"-p", "example.com/app/articles",
		"-o", "/tmp/out.a",
		"-importcfg", "/tmp/b001/importcfg",
		"-trimpath", "/x",
		"/src/articles/handler.go",
		"/src/articles/model.go",
		"-buildid", "abc",
	}
	goFiles, pkgPath, importcfg := parseCompileArgs(args)
	if pkgPath != "example.com/app/articles" {
		t.Errorf("pkgPath = %q", pkgPath)
	}
	if importcfg != "/tmp/b001/importcfg" {
		t.Errorf("importcfg = %q", importcfg)
	}
	if len(goFiles) != 2 || goFiles[0] != "/src/articles/handler.go" || goFiles[1] != "/src/articles/model.go" {
		t.Errorf("goFiles = %v", goFiles)
	}
}

func TestShouldSkipPackage_IncludePrefix(t *testing.T) {
	t.Setenv("SF_INSTRUMENT_INCLUDE_PREFIX", "example.com/app")
	cases := []struct {
		pkg  string
		skip bool
	}{
		{"example.com/app", false},          // exact match → instrument
		{"example.com/app/articles", false}, // under prefix → instrument
		{"example.com/application", true},   // prefix-but-not-segment → skip (no false match)
		{"github.com/mattn/go-sqlite3", true},
		{"fmt", true},                                       // stdlib
		{"github.com/SailfishAI/sf-veritas-go", true},       // self
		{"example.com/app/internal/sfveritashelpers", true}, // contains 'sfveritas' → self-skip guard
	}
	for _, c := range cases {
		if got := shouldSkipPackage(c.pkg); got != c.skip {
			t.Errorf("shouldSkipPackage(%q) = %v, want %v", c.pkg, got, c.skip)
		}
	}
}

func TestReadModulePath(t *testing.T) {
	dir := t.TempDir()
	gomod := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(gomod, []byte("module example.com/myapp\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readModulePath(gomod); got != "example.com/myapp" {
		t.Errorf("readModulePath = %q", got)
	}
	if got := readModulePath(""); got != "" {
		t.Errorf("readModulePath(empty) = %q", got)
	}
	if got := readModulePath(filepath.Join(dir, "missing.mod")); got != "" {
		t.Errorf("readModulePath(missing) = %q", got)
	}
}

func TestIsCgoFile(t *testing.T) {
	dir := t.TempDir()

	// A file importing "C" is CGO.
	cgoSrc := "package x\n\n/*\n#include <stdio.h>\n*/\nimport \"C\"\n\nfunc f() { C.printf(nil) }\n"
	cgoPath := filepath.Join(dir, "cgo_user.go")
	if err := os.WriteFile(cgoPath, []byte(cgoSrc), 0644); err != nil {
		t.Fatal(err)
	}
	node, err := parser.ParseFile(token.NewFileSet(), cgoPath, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if !isCgoFile(cgoPath, node) {
		t.Errorf("expected import-\"C\" file to be detected as CGO")
	}

	// A generated _cgo_gotypes.go is CGO by filename.
	genPath := filepath.Join(dir, "_cgo_gotypes.go")
	if err := os.WriteFile(genPath, []byte("package x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	genNode, _ := parser.ParseFile(token.NewFileSet(), genPath, nil, 0)
	if !isCgoFile(genPath, genNode) {
		t.Errorf("expected _cgo_gotypes.go to be detected as CGO")
	}

	// A file containing a //go:cgo_ directive is CGO.
	dirPath := filepath.Join(dir, "trampoline.go")
	if err := os.WriteFile(dirPath, []byte("package x\n\n//go:cgo_import_static foo\nfunc g() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dirNode, _ := parser.ParseFile(token.NewFileSet(), dirPath, nil, parser.ParseComments)
	if !isCgoFile(dirPath, dirNode) {
		t.Errorf("expected //go:cgo_ directive file to be detected as CGO")
	}

	// A plain Go file is NOT CGO.
	plainPath := filepath.Join(dir, "plain.go")
	if err := os.WriteFile(plainPath, []byte("package x\n\nfunc h() int { return 1 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	plainNode, _ := parser.ParseFile(token.NewFileSet(), plainPath, nil, 0)
	if isCgoFile(plainPath, plainNode) {
		t.Errorf("plain Go file should not be CGO")
	}
}

func TestReadPackagefileLine(t *testing.T) {
	dir := t.TempDir()
	sfLine := "packagefile github.com/SailfishAI/sf-veritas-go=/cache/abc/_pkg_.a"
	cfg := filepath.Join(dir, "importcfg")
	if err := os.WriteFile(cfg, []byte("# import config\npackagefile fmt=/std/fmt.a\n"+sfLine+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Present → returned verbatim.
	if got := readPackagefileLine(cfg, sfveritasImportPath); got != sfLine {
		t.Errorf("readPackagefileLine(present) = %q", got)
	}
	// Absent → "".
	if got := readPackagefileLine(cfg, "github.com/nope/missing"); got != "" {
		t.Errorf("readPackagefileLine(absent) = %q", got)
	}
	// Missing/empty importcfg path → "".
	if got := readPackagefileLine("", sfveritasImportPath); got != "" {
		t.Errorf("readPackagefileLine(empty path) = %q", got)
	}
}
