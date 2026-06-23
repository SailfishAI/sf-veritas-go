// sfveritas-instrument is a Go toolexec wrapper that automatically instruments
// Go source files with Sailfish telemetry. It intercepts the Go compiler,
// parses source AST, injects function span tracking (arguments, return values,
// and panic capture), then passes the modified source to the real compiler.
//
// Usage:
//
//	go build -toolexec="sfveritas-instrument" ./...
//	go run -toolexec="sfveritas-instrument" .
//	go test -toolexec="sfveritas-instrument" ./...
//
// Scope: by default ONLY the main module's own packages are instrumented (the
// module path is read from $GOMOD). Standard library, third-party dependencies,
// and CGO-generated files are passed through unmodified. Set
// SF_INSTRUMENT_INCLUDE_PREFIX to an import-path prefix to override the scope.
//
// Safety / coverage: the wrapper NEVER emits uncompilable code. To use the
// injected sfveritas import, the compiler's -importcfg for that package must map
// the import path to a compiled archive. Go only provides that entry for packages
// that actually import sfveritas, and the archive paths are ephemeral per-compile
// artifacts (they cannot be borrowed across compile actions). So we instrument a
// package ONLY when sfveritas is already resolvable in its importcfg — i.e. some
// file in the package already imports it (e.g. for SetupInterceptors or a manual
// TraceFunc span). Packages that don't reference sfveritas are skipped. This is
// the only sound rule via toolexec; for guaranteed coverage of a specific
// function use a manual sfveritas.TraceFunc / TraceFuncWithArgs span.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const sfveritasImportPath = "github.com/SailfishAI/sf-veritas-go"

func debugEnabled() bool { return os.Getenv("SF_INSTRUMENT_DEBUG") == "true" }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "sfveritas-instrument: usage: sfveritas-instrument <tool> [args...]\n")
		os.Exit(1)
	}

	tool := os.Args[1]
	args := os.Args[2:]

	// Only intercept the compile tool — pass everything else through.
	toolBase := filepath.Base(tool)
	if toolBase != "compile" && !strings.HasSuffix(toolBase, ".compile") {
		passthrough(tool, args)
		return
	}

	goFiles, pkgPath, importcfgPath := parseCompileArgs(args)

	// Skip stdlib, deps, our own package, and empty compiles.
	if shouldSkipPackage(pkgPath) || len(goFiles) == 0 {
		passthrough(tool, args)
		return
	}

	// Only instrument when the sfveritas import is already resolvable for this
	// package (its importcfg maps the import path to a real archive). Injecting it
	// otherwise would not compile. This means a package opts into auto-instrumentation
	// by importing sfveritas somewhere (e.g. SetupInterceptors / a manual span).
	if readPackagefileLine(importcfgPath, sfveritasImportPath) == "" {
		if debugEnabled() {
			fmt.Fprintf(os.Stderr, "[sfveritas-instrument] skip %s: package does not import sfveritas (no resolvable import to inject)\n", pkgPath)
		}
		passthrough(tool, args)
		return
	}

	// Per-invocation work dir for rewritten files — avoids cross-compile temp-file
	// collisions for identically named files under parallel builds.
	workDir, err := os.MkdirTemp("", "sfveritas-")
	if err != nil {
		passthrough(tool, args)
		return
	}

	modified := false
	for i, goFile := range goFiles {
		newPath, err := instrumentFile(goFile, workDir)
		if err != nil {
			if debugEnabled() {
				fmt.Fprintf(os.Stderr, "[sfveritas-instrument] skip %s: %v\n", goFile, err)
			}
			continue
		}
		if newPath != goFile {
			replaceArg(args, goFile, newPath)
			goFiles[i] = newPath
			modified = true
		}
	}

	if modified && debugEnabled() {
		fmt.Fprintf(os.Stderr, "[sfveritas-instrument] instrumented package %s (%d files)\n", pkgPath, len(goFiles))
	}

	passthrough(tool, args)
}

// passthrough executes the original tool with given arguments.
func passthrough(tool string, args []string) {
	cmd := exec.Command(tool, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "sfveritas-instrument: %v\n", err)
		os.Exit(1)
	}
}

// parseCompileArgs extracts Go source file paths, the package import path, and
// the -importcfg path from the compile command arguments.
func parseCompileArgs(args []string) (goFiles []string, pkgPath string, importcfgPath string) {
	skipNext := false
	for i, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "-p" && i+1 < len(args) {
			pkgPath = args[i+1]
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-p=") {
			pkgPath = strings.TrimPrefix(arg, "-p=")
			continue
		}
		if arg == "-importcfg" && i+1 < len(args) {
			importcfgPath = args[i+1]
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-importcfg=") {
			importcfgPath = strings.TrimPrefix(arg, "-importcfg=")
			continue
		}
		// Skip other flags with values.
		if arg == "-o" || arg == "-trimpath" || arg == "-I" || arg == "-D" ||
			arg == "-embedcfg" || arg == "-lang" ||
			arg == "-goversion" || arg == "-symabis" || arg == "-asmhdr" ||
			arg == "-buildid" || arg == "-pgoprofile" {
			skipNext = true
			continue
		}
		if !strings.HasPrefix(arg, "-") && strings.HasSuffix(arg, ".go") {
			goFiles = append(goFiles, arg)
		}
	}
	return
}

// readPackagefileLine returns the `packagefile <pkg>=<path>` line for pkg from an
// importcfg file, or "" if absent/unreadable.
func readPackagefileLine(importcfgPath, pkg string) string {
	if importcfgPath == "" {
		return ""
	}
	data, err := os.ReadFile(importcfgPath)
	if err != nil {
		return ""
	}
	prefix := "packagefile " + pkg + "="
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

var (
	mainModuleOnce sync.Once
	mainModuleVal  string
)

// mainModulePath returns the module path of the main module being built, read
// from the go.mod that $GOMOD points at. Returns "" if unavailable.
func mainModulePath() string {
	mainModuleOnce.Do(func() {
		mainModuleVal = readModulePath(os.Getenv("GOMOD"))
	})
	return mainModuleVal
}

// readModulePath returns the `module <path>` declared in the go.mod at gomodPath,
// or "" if unavailable. Pure (testable) — mainModulePath memoizes it.
func readModulePath(gomodPath string) string {
	if gomodPath == "" || gomodPath == "/dev/null" || gomodPath == os.DevNull {
		return ""
	}
	data, err := os.ReadFile(gomodPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// shouldSkipPackage returns true for packages we should not instrument.
// Precedence: never-instrument set (sfveritas, stdlib) > explicit
// SF_INSTRUMENT_INCLUDE_PREFIX allowlist > main-module-only > legacy dep skip.
func shouldSkipPackage(pkgPath string) bool {
	if pkgPath == "" {
		return true
	}
	// Never instrument the sfveritas library itself (infinite recursion).
	if strings.Contains(pkgPath, "sf-veritas-go") || strings.Contains(pkgPath, "sfveritas") {
		return true
	}
	// Standard library: no dot and no slash in the import path.
	if !strings.Contains(pkgPath, ".") && !strings.Contains(pkgPath, "/") {
		return true
	}
	stdPrefixes := []string{
		"runtime", "internal/", "reflect", "sync", "syscall",
		"os", "io", "fmt", "strings", "bytes", "strconv",
		"encoding/", "crypto/", "net/", "math/", "time",
		"context", "errors", "sort", "unicode",
		"log/", "path/", "regexp", "testing",
		"debug/", "go/", "text/", "html/", "image/",
		"archive/", "compress/", "database/", "embed",
		"hash/", "index/", "mime/", "plugin",
		"vendor/", // stdlib-internal vendored packages (e.g. vendor/golang.org/x/...)
	}
	for _, prefix := range stdPrefixes {
		if pkgPath == prefix || strings.HasPrefix(pkgPath, prefix) {
			return true
		}
	}

	// Explicit allowlist prefix wins when set: instrument ONLY matching paths.
	if inc := os.Getenv("SF_INSTRUMENT_INCLUDE_PREFIX"); inc != "" {
		return !(pkgPath == inc || strings.HasPrefix(pkgPath, inc+"/"))
	}

	// Default: instrument ONLY the main module's own packages. This keeps us out
	// of third-party deps (notably CGO packages like mattn/go-sqlite3 whose
	// generated files cannot be rewritten).
	if main := mainModulePath(); main != "" {
		return !(pkgPath == main || strings.HasPrefix(pkgPath, main+"/"))
	}

	// Main module unknown (e.g. GOPATH mode): fall back to skipping known
	// dependency hosts and instrument the rest (legacy behavior).
	if strings.HasPrefix(pkgPath, "golang.org/") || strings.HasPrefix(pkgPath, "google.golang.org/") {
		return true
	}
	return false
}

// replaceArg replaces oldVal with newVal in the args slice.
func replaceArg(args []string, oldVal, newVal string) {
	for i, a := range args {
		if a == oldVal {
			args[i] = newVal
			return
		}
	}
}

// instrumentFile parses a Go source file and injects Sailfish instrumentation.
// Returns the path to the modified file (in workDir), or the original path if no
// modification was needed/safe. The caller guarantees the package's importcfg
// resolves sfveritas, so adding/using the import here is sound.
func instrumentFile(filePath, workDir string) (string, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return filePath, fmt.Errorf("parse: %w", err)
	}

	// Never rewrite CGO files: re-emitting them via format.Node breaks
	// //go:cgo_* directives ("only allowed in cgo-generated code").
	if isCgoFile(filePath, node) {
		return filePath, nil
	}

	funcs := findInstrumentableFunctions(node)
	if len(funcs) == 0 {
		return filePath, nil
	}

	// Ensure this file binds the `sfveritas` name. The package importcfg already
	// resolves the path (caller guarantees it); we only add the file-level import
	// if this particular file doesn't already have it. The ONLY import we ever
	// inject is sfveritas (context-less functions use StartSpanNoCtx, so no
	// `context` import is needed).
	if !hasImport(node, sfveritasImportPath) {
		addImport(node, "sfveritas", sfveritasImportPath)
	}

	for _, fn := range funcs {
		instrumentFunction(fn)
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return filePath, fmt.Errorf("format: %w", err)
	}

	tmpFile := filepath.Join(workDir, filepath.Base(filePath))
	if err := os.WriteFile(tmpFile, buf.Bytes(), 0644); err != nil {
		return filePath, fmt.Errorf("write: %w", err)
	}
	return tmpFile, nil
}

// isCgoFile reports whether a file is CGO-related and must not be rewritten.
func isCgoFile(filePath string, node *ast.File) bool {
	base := filepath.Base(filePath)
	if strings.HasPrefix(base, "_cgo") || strings.Contains(base, "_cgo_") {
		return true
	}
	if hasImport(node, "C") {
		return true
	}
	if data, err := os.ReadFile(filePath); err == nil {
		if bytes.Contains(data, []byte("//go:cgo_")) {
			return true
		}
	}
	return false
}

// hasImport checks if the file already imports a given path.
func hasImport(node *ast.File, importPath string) bool {
	for _, imp := range node.Imports {
		if imp.Path.Value == `"`+importPath+`"` {
			return true
		}
	}
	return false
}

// addImport adds an import declaration to the file.
func addImport(node *ast.File, name, path string) {
	importSpec := &ast.ImportSpec{
		Name: ast.NewIdent(name),
		Path: &ast.BasicLit{
			Kind:  token.STRING,
			Value: `"` + path + `"`,
		},
	}
	if len(node.Decls) > 0 {
		if genDecl, ok := node.Decls[0].(*ast.GenDecl); ok && genDecl.Tok == token.IMPORT {
			genDecl.Specs = append(genDecl.Specs, importSpec)
			return
		}
	}
	importDecl := &ast.GenDecl{
		Tok:   token.IMPORT,
		Specs: []ast.Spec{importSpec},
	}
	node.Decls = append([]ast.Decl{importDecl}, node.Decls...)
}

// findInstrumentableFunctions returns all functions/methods in the file that
// should be instrumented. Skips init() and trivially short functions.
func findInstrumentableFunctions(node *ast.File) []*ast.FuncDecl {
	var funcs []*ast.FuncDecl
	for _, decl := range node.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		if name == "init" || name == "_" {
			continue
		}
		// Skip very short functions (1 statement or less) — not worth the overhead.
		if len(fn.Body.List) <= 1 {
			continue
		}
		funcs = append(funcs, fn)
	}
	return funcs
}

// instrumentFunction injects span tracking into a function body:
//  1. _sfSpan := sfveritas.StartSpanWithArgs(ctx, "Name", args)  (or StartSpanNoCtx if no ctx param)
//  2. a deferred recover that ends the span and reports panics
//  3. named return values so the deferred End() can capture them
func instrumentFunction(fn *ast.FuncDecl) {
	funcName := fn.Name.Name
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		recvType := exprToString(fn.Recv.List[0].Type)
		funcName = recvType + "." + funcName
	}

	ctxParamName := findContextParam(fn)
	argNames := collectParamNames(fn)
	returnVarNames := ensureNamedReturns(fn)
	argsExpr := buildArgsMapExpr(argNames)

	var stmts []ast.Stmt

	// 1. Start span. Functions with a context param thread it through; context-less
	// functions use StartSpanNoCtx so we never need to inject a `context` import.
	var spanRhs ast.Expr
	if ctxParamName != "" {
		spanRhs = &ast.CallExpr{
			Fun: &ast.SelectorExpr{X: ast.NewIdent("sfveritas"), Sel: ast.NewIdent("StartSpanWithArgs")},
			Args: []ast.Expr{
				ast.NewIdent(ctxParamName),
				&ast.BasicLit{Kind: token.STRING, Value: `"` + funcName + `"`},
				argsExpr,
			},
		}
	} else {
		spanRhs = &ast.CallExpr{
			Fun: &ast.SelectorExpr{X: ast.NewIdent("sfveritas"), Sel: ast.NewIdent("StartSpanNoCtx")},
			Args: []ast.Expr{
				&ast.BasicLit{Kind: token.STRING, Value: `"` + funcName + `"`},
				argsExpr,
			},
		}
	}
	stmts = append(stmts, &ast.AssignStmt{
		Lhs: []ast.Expr{ast.NewIdent("_sfSpan")},
		Tok: token.DEFINE,
		Rhs: []ast.Expr{spanRhs},
	})

	// 2. If the function has a context param, update it with the span's context.
	if ctxParamName != "" {
		stmts = append(stmts, &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent(ctxParamName)},
			Tok: token.ASSIGN,
			Rhs: []ast.Expr{
				&ast.CallExpr{Fun: &ast.SelectorExpr{X: ast.NewIdent("_sfSpan"), Sel: ast.NewIdent("Context")}},
			},
		})
	}

	// 3. Defer: end span + report panics with params/named-returns as context.
	stmts = append(stmts, buildDeferStmt(argNames, returnVarNames))

	fn.Body.List = append(stmts, fn.Body.List...)
}

// ensureNamedReturns ensures all return values have names so they can be
// referenced in the defer for return value capture. Returns all return variable
// names, or nil if the function has no return values.
func ensureNamedReturns(fn *ast.FuncDecl) []string {
	results := fn.Type.Results
	if results == nil || len(results.List) == 0 {
		return nil
	}

	var names []string
	retIdx := 0
	for _, field := range results.List {
		if len(field.Names) == 0 {
			name := "_sfRet" + strconv.Itoa(retIdx)
			field.Names = []*ast.Ident{ast.NewIdent(name)}
			names = append(names, name)
			retIdx++
		} else {
			for _, n := range field.Names {
				names = append(names, n.Name)
				retIdx++
			}
		}
	}
	return names
}

// findContextParam returns the name of the first context.Context parameter, or "".
func findContextParam(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		typeStr := exprToString(field.Type)
		if typeStr == "context.Context" || typeStr == "Context" {
			if len(field.Names) > 0 {
				return field.Names[0].Name
			}
		}
	}
	return ""
}

// collectParamNames returns all named parameters of a function (excluding context).
func collectParamNames(fn *ast.FuncDecl) []string {
	if fn.Type.Params == nil {
		return nil
	}
	var names []string
	for _, field := range fn.Type.Params.List {
		typeStr := exprToString(field.Type)
		if typeStr == "context.Context" || typeStr == "Context" {
			continue
		}
		for _, name := range field.Names {
			if name.Name != "_" {
				names = append(names, name.Name)
			}
		}
	}
	return names
}

// buildArgsMapExpr builds a map[string]interface{}{...} expression from parameter names.
func buildArgsMapExpr(paramNames []string) ast.Expr {
	return buildStringIfaceMap(paramNames, func(name string) string { return name })
}

// buildReturnValueMapExpr builds a map[string]interface{}{...} expression from
// return variable names, keyed by position index (matches the backend format).
func buildReturnValueMapExpr(returnVarNames []string) ast.Expr {
	keys := make([]string, len(returnVarNames))
	for i := range returnVarNames {
		keys[i] = strconv.Itoa(i)
	}
	return buildKeyedStringIfaceMap(keys, returnVarNames)
}

// stringIfaceMapType returns the AST for map[string]interface{}.
func stringIfaceMapType() *ast.MapType {
	return &ast.MapType{
		Key:   ast.NewIdent("string"),
		Value: &ast.InterfaceType{Methods: &ast.FieldList{}},
	}
}

// buildStringIfaceMap builds map[string]interface{}{"<key(v)>": v, ...} from values.
func buildStringIfaceMap(values []string, key func(string) string) ast.Expr {
	keys := make([]string, len(values))
	for i, v := range values {
		keys[i] = key(v)
	}
	return buildKeyedStringIfaceMap(keys, values)
}

// buildKeyedStringIfaceMap builds map[string]interface{}{"<keys[i]>": idents[i], ...}.
func buildKeyedStringIfaceMap(keys, idents []string) ast.Expr {
	elts := make([]ast.Expr, len(idents))
	for i := range idents {
		elts[i] = &ast.KeyValueExpr{
			Key:   &ast.BasicLit{Kind: token.STRING, Value: `"` + keys[i] + `"`},
			Value: ast.NewIdent(idents[i]),
		}
	}
	return &ast.CompositeLit{Type: stringIfaceMapType(), Elts: elts}
}

// buildDeferStmt builds the deferred func that recovers from panics (reporting
// params + named return values as context) and ends the span with return values.
// It captures ONLY parameters and named return values — both are guaranteed to be
// in scope at the top of the function where the defer is prepended (arbitrary
// locals are NOT, and referencing them would not compile).
func buildDeferStmt(paramNames, returnVarNames []string) *ast.DeferStmt {
	panicContextNames := append(append([]string{}, paramNames...), returnVarNames...)
	panicContextExpr := buildStringIfaceMap(panicContextNames, func(name string) string { return name })

	// Normal-exit End() argument:
	//   0 returns: nil
	//   1 return:  _sfRet0      (matches JS/TS single-value format)
	//   2+ returns: map{"0": _sfRet0, "1": _sfRet1, ...}
	var normalEndArg ast.Expr
	switch len(returnVarNames) {
	case 0:
		normalEndArg = ast.NewIdent("nil")
	case 1:
		normalEndArg = ast.NewIdent(returnVarNames[0])
	default:
		normalEndArg = buildReturnValueMapExpr(returnVarNames)
	}

	body := &ast.BlockStmt{
		List: []ast.Stmt{
			&ast.IfStmt{
				Init: &ast.AssignStmt{
					Lhs: []ast.Expr{ast.NewIdent("_sfR")},
					Tok: token.DEFINE,
					Rhs: []ast.Expr{&ast.CallExpr{Fun: ast.NewIdent("recover")}},
				},
				Cond: &ast.BinaryExpr{X: ast.NewIdent("_sfR"), Op: token.NEQ, Y: ast.NewIdent("nil")},
				Body: &ast.BlockStmt{
					List: []ast.Stmt{
						&ast.ExprStmt{X: &ast.CallExpr{
							Fun: &ast.SelectorExpr{X: ast.NewIdent("sfveritas"), Sel: ast.NewIdent("TransmitPanicWithLocals")},
							Args: []ast.Expr{
								&ast.CallExpr{Fun: &ast.SelectorExpr{X: ast.NewIdent("_sfSpan"), Sel: ast.NewIdent("Context")}},
								ast.NewIdent("_sfR"),
								panicContextExpr,
							},
						}},
						&ast.ExprStmt{X: &ast.CallExpr{
							Fun:  &ast.SelectorExpr{X: ast.NewIdent("_sfSpan"), Sel: ast.NewIdent("End")},
							Args: []ast.Expr{ast.NewIdent("nil")},
						}},
						&ast.ExprStmt{X: &ast.CallExpr{
							Fun:  ast.NewIdent("panic"),
							Args: []ast.Expr{ast.NewIdent("_sfR")},
						}},
					},
				},
			},
			&ast.ExprStmt{X: &ast.CallExpr{
				Fun:  &ast.SelectorExpr{X: ast.NewIdent("_sfSpan"), Sel: ast.NewIdent("End")},
				Args: []ast.Expr{normalEndArg},
			}},
		},
	}

	return &ast.DeferStmt{
		Call: &ast.CallExpr{
			Fun: &ast.FuncLit{
				Type: &ast.FuncType{Params: &ast.FieldList{}},
				Body: body,
			},
		},
	}
}

// exprToString converts an AST expression to its string representation.
func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprToString(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return "*" + exprToString(e.X)
	case *ast.ArrayType:
		return "[]" + exprToString(e.Elt)
	case *ast.MapType:
		return "map[" + exprToString(e.Key) + "]" + exprToString(e.Value)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return "unknown"
	}
}
