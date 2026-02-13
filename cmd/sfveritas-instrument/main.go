// sfveritas-instrument is a Go toolexec wrapper that automatically instruments
// Go source files with Sailfish telemetry. It intercepts the Go compiler,
// parses source AST, injects function span tracking (arguments, return values,
// and local variables on panic), then passes the modified source to the real compiler.
//
// Usage:
//
//	go build -toolexec="sfveritas-instrument" ./...
//	go run -toolexec="sfveritas-instrument" .
//	go test -toolexec="sfveritas-instrument" ./...
//
// The tool only modifies compilation of user packages — standard library and
// dependency packages are passed through unmodified.
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
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "sfveritas-instrument: usage: sfveritas-instrument <tool> [args...]\n")
		os.Exit(1)
	}

	tool := os.Args[1]
	args := os.Args[2:]

	// Only intercept the compile tool — pass everything else through
	toolBase := filepath.Base(tool)
	if toolBase != "compile" && !strings.HasSuffix(toolBase, ".compile") {
		passthrough(tool, args)
		return
	}

	// Parse compile flags to find Go source files and the package path
	goFiles, pkgPath := parseCompileArgs(args)

	// Skip instrumentation for standard library, dependencies, and our own package
	if shouldSkipPackage(pkgPath) || len(goFiles) == 0 {
		passthrough(tool, args)
		return
	}

	// Instrument each Go source file
	modified := false
	for i, goFile := range goFiles {
		newPath, err := instrumentFile(goFile, pkgPath)
		if err != nil {
			if os.Getenv("SF_INSTRUMENT_DEBUG") == "true" {
				fmt.Fprintf(os.Stderr, "[sfveritas-instrument] skip %s: %v\n", goFile, err)
			}
			continue
		}
		if newPath != goFile {
			// Replace the source file path in args
			replaceArg(args, goFile, newPath)
			goFiles[i] = newPath
			modified = true
		}
	}

	if modified && os.Getenv("SF_INSTRUMENT_DEBUG") == "true" {
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

// parseCompileArgs extracts Go source file paths and the package import path
// from the compile command arguments.
func parseCompileArgs(args []string) (goFiles []string, pkgPath string) {
	skipNext := false
	for i, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		// Flags that take a value
		if arg == "-p" && i+1 < len(args) {
			pkgPath = args[i+1]
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-p=") {
			pkgPath = strings.TrimPrefix(arg, "-p=")
			continue
		}
		// Skip other flags with values
		if arg == "-o" || arg == "-trimpath" || arg == "-I" || arg == "-D" ||
			arg == "-importcfg" || arg == "-embedcfg" || arg == "-lang" ||
			arg == "-goversion" || arg == "-symabis" || arg == "-asmhdr" ||
			arg == "-buildid" || arg == "-pgoprofile" {
			skipNext = true
			continue
		}
		// Detect Go source files (non-flag args ending in .go)
		if !strings.HasPrefix(arg, "-") && strings.HasSuffix(arg, ".go") {
			goFiles = append(goFiles, arg)
		}
	}
	return
}

// shouldSkipPackage returns true for packages we should not instrument.
func shouldSkipPackage(pkgPath string) bool {
	if pkgPath == "" {
		return true
	}
	// Skip standard library
	if !strings.Contains(pkgPath, ".") && !strings.Contains(pkgPath, "/") {
		return true
	}
	// Known standard library prefixes
	stdPrefixes := []string{
		"runtime", "internal/", "reflect", "sync", "syscall",
		"os", "io", "fmt", "strings", "bytes", "strconv",
		"encoding/", "crypto/", "net/", "math/", "time",
		"context", "errors", "sort", "unicode",
		"log/", "path/", "regexp", "testing",
		"debug/", "go/", "text/", "html/", "image/",
		"archive/", "compress/", "database/", "embed",
		"hash/", "index/", "mime/", "plugin",
	}
	for _, prefix := range stdPrefixes {
		if pkgPath == prefix || strings.HasPrefix(pkgPath, prefix) {
			return true
		}
	}
	// Skip the sfveritas library itself to avoid infinite recursion
	if strings.Contains(pkgPath, "sf-veritas-go") || strings.Contains(pkgPath, "sfveritas") {
		return true
	}
	// Skip common dependency paths
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
// Returns the path to the modified file (in a temp directory), or the original
// path if no modification was needed.
func instrumentFile(filePath, pkgPath string) (string, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return filePath, fmt.Errorf("parse: %w", err)
	}

	// Check if file already imports sfveritas (manually instrumented)
	if hasImport(node, "github.com/SailfishAI/sf-veritas-go") {
		return filePath, nil
	}

	// Find functions to instrument
	funcs := findInstrumentableFunctions(node)
	if len(funcs) == 0 {
		return filePath, nil
	}

	// Add sfveritas import
	addImport(node, "sfveritas", "github.com/SailfishAI/sf-veritas-go")

	// Instrument each function
	for _, fn := range funcs {
		instrumentFunction(fset, fn, pkgPath)
	}

	// Write modified AST to temp file
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return filePath, fmt.Errorf("format: %w", err)
	}

	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, "sfveritas_"+filepath.Base(filePath))
	if err := os.WriteFile(tmpFile, buf.Bytes(), 0644); err != nil {
		return filePath, fmt.Errorf("write: %w", err)
	}

	return tmpFile, nil
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
	// Add to existing import block or create new one
	if len(node.Decls) > 0 {
		if genDecl, ok := node.Decls[0].(*ast.GenDecl); ok && genDecl.Tok == token.IMPORT {
			genDecl.Specs = append(genDecl.Specs, importSpec)
			return
		}
	}
	// Create new import declaration
	importDecl := &ast.GenDecl{
		Tok:   token.IMPORT,
		Specs: []ast.Spec{importSpec},
	}
	node.Decls = append([]ast.Decl{importDecl}, node.Decls...)
}

// findInstrumentableFunctions returns all functions/methods in the file that
// should be instrumented. Skips init(), main() setup, and test helpers.
func findInstrumentableFunctions(node *ast.File) []*ast.FuncDecl {
	var funcs []*ast.FuncDecl
	for _, decl := range node.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		// Skip functions that shouldn't be instrumented
		if name == "init" || name == "_" {
			continue
		}
		// Skip very short functions (1 statement or less) — not worth the overhead
		if len(fn.Body.List) <= 1 {
			continue
		}
		// Skip functions that don't take context (they can't propagate spans)
		// But instrument all functions — the injected code uses context.Background()
		// as fallback for functions without context
		funcs = append(funcs, fn)
	}
	return funcs
}

// instrumentFunction injects span tracking into a function body.
// It adds:
//  1. A sfveritas.StartSpanWithArgs() call at function entry
//  2. A defer that captures local variables on panic and ends the span
//  3. Return value capture via named return variables
func instrumentFunction(fset *token.FileSet, fn *ast.FuncDecl, pkgPath string) {
	funcName := fn.Name.Name
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		// Method — prefix with receiver type
		recvType := exprToString(fn.Recv.List[0].Type)
		funcName = recvType + "." + funcName
	}

	// Determine if function has a context.Context parameter
	ctxParamName := findContextParam(fn)

	// Collect parameter names for argument capture
	argNames := collectParamNames(fn)

	// Collect local variable names declared in the function body
	localVarNames := collectLocalVarNames(fn.Body)

	// Name unnamed return values for capture via defer
	returnVarNames := ensureNamedReturns(fn)

	// Build the injected statements
	var stmts []ast.Stmt

	// 1. Context: use the context param if available, otherwise context.Background()
	ctxVar := ctxParamName
	if ctxVar == "" {
		ctxVar = "_sfCtx"
		stmts = append(stmts, parseStmt(`_sfCtx := context.Background()`))
	}

	// 2. Build args map expression
	argsExpr := buildArgsMapExpr(argNames)

	// 3. Start span: _sfSpan := sfveritas.StartSpanWithArgs(ctx, "FuncName", argsMap)
	spanStmt := &ast.AssignStmt{
		Lhs: []ast.Expr{ast.NewIdent("_sfSpan")},
		Tok: token.DEFINE,
		Rhs: []ast.Expr{
			&ast.CallExpr{
				Fun: &ast.SelectorExpr{
					X:   ast.NewIdent("sfveritas"),
					Sel: ast.NewIdent("StartSpanWithArgs"),
				},
				Args: []ast.Expr{
					ast.NewIdent(ctxVar),
					&ast.BasicLit{Kind: token.STRING, Value: `"` + funcName + `"`},
					argsExpr,
				},
			},
		},
	}
	stmts = append(stmts, spanStmt)

	// 4. If the function has a context param, update it with the span's context
	if ctxParamName != "" {
		stmts = append(stmts, &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent(ctxParamName)},
			Tok: token.ASSIGN,
			Rhs: []ast.Expr{
				&ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   ast.NewIdent("_sfSpan"),
						Sel: ast.NewIdent("Context"),
					},
				},
			},
		})
	}

	// 5. Defer: captures locals on panic, ends span with return values
	deferStmt := buildDeferStmt(localVarNames, argNames, returnVarNames)
	stmts = append(stmts, deferStmt)

	// Prepend our statements to the function body
	fn.Body.List = append(stmts, fn.Body.List...)
}

// ensureNamedReturns ensures all return values have names so they can be
// referenced in the defer for return value capture. Returns the list of
// all return variable names, or nil if the function has no return values.
func ensureNamedReturns(fn *ast.FuncDecl) []string {
	results := fn.Type.Results
	if results == nil || len(results.List) == 0 {
		return nil
	}

	var names []string
	retIdx := 0

	for _, field := range results.List {
		if len(field.Names) == 0 {
			// Unnamed return value — add a synthetic name
			name := "_sfRet" + strconv.Itoa(retIdx)
			field.Names = []*ast.Ident{ast.NewIdent(name)}
			names = append(names, name)
			retIdx++
		} else {
			// Already named — use existing names
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

// collectParamNames returns all named parameters of a function.
func collectParamNames(fn *ast.FuncDecl) []string {
	if fn.Type.Params == nil {
		return nil
	}
	var names []string
	for _, field := range fn.Type.Params.List {
		typeStr := exprToString(field.Type)
		// Skip context params — they're not interesting as arguments
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

// collectLocalVarNames returns all variable names declared via := or var in a function body.
// Only collects top-level locals (not nested in if/for/etc.) for simplicity.
func collectLocalVarNames(body *ast.BlockStmt) []string {
	var names []string
	seen := map[string]bool{}
	for _, stmt := range body.List {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				for _, lhs := range s.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name != "_" {
						if !seen[ident.Name] {
							names = append(names, ident.Name)
							seen[ident.Name] = true
						}
					}
				}
			}
		case *ast.DeclStmt:
			if genDecl, ok := s.Decl.(*ast.GenDecl); ok && genDecl.Tok == token.VAR {
				for _, spec := range genDecl.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range vs.Names {
							if name.Name != "_" && !seen[name.Name] {
								names = append(names, name.Name)
								seen[name.Name] = true
							}
						}
					}
				}
			}
		}
	}
	return names
}

// buildArgsMapExpr builds a map[string]interface{}{...} expression from parameter names.
func buildArgsMapExpr(paramNames []string) ast.Expr {
	if len(paramNames) == 0 {
		return &ast.CompositeLit{
			Type: &ast.MapType{
				Key:   ast.NewIdent("string"),
				Value: &ast.InterfaceType{Methods: &ast.FieldList{}},
			},
		}
	}
	elts := make([]ast.Expr, len(paramNames))
	for i, name := range paramNames {
		elts[i] = &ast.KeyValueExpr{
			Key:   &ast.BasicLit{Kind: token.STRING, Value: `"` + name + `"`},
			Value: ast.NewIdent(name),
		}
	}
	return &ast.CompositeLit{
		Type: &ast.MapType{
			Key:   ast.NewIdent("string"),
			Value: &ast.InterfaceType{Methods: &ast.FieldList{}},
		},
		Elts: elts,
	}
}

// buildReturnValueMapExpr builds a map[string]interface{}{...} expression
// from return variable names. Uses the position index as the key for synthetic
// names, or the original name for named returns.
func buildReturnValueMapExpr(returnVarNames []string) ast.Expr {
	elts := make([]ast.Expr, len(returnVarNames))
	for i, name := range returnVarNames {
		// Use index as key for consistency with the backend
		key := strconv.Itoa(i)
		elts[i] = &ast.KeyValueExpr{
			Key:   &ast.BasicLit{Kind: token.STRING, Value: `"` + key + `"`},
			Value: ast.NewIdent(name),
		}
	}
	return &ast.CompositeLit{
		Type: &ast.MapType{
			Key:   ast.NewIdent("string"),
			Value: &ast.InterfaceType{Methods: &ast.FieldList{}},
		},
		Elts: elts,
	}
}

// buildDeferStmt builds the defer statement that:
// - Recovers from panics and captures local variables
// - Ends the span with return value map (or nil if no return values)
func buildDeferStmt(localVarNames, paramNames, returnVarNames []string) *ast.DeferStmt {
	// Build the locals map for panic capture
	var localsElts []ast.Expr
	for _, name := range localVarNames {
		localsElts = append(localsElts, &ast.KeyValueExpr{
			Key:   &ast.BasicLit{Kind: token.STRING, Value: `"` + name + `"`},
			Value: ast.NewIdent(name),
		})
	}

	localsMapExpr := &ast.CompositeLit{
		Type: &ast.MapType{
			Key:   ast.NewIdent("string"),
			Value: &ast.InterfaceType{Methods: &ast.FieldList{}},
		},
		Elts: localsElts,
	}

	// Build the return value expression for End()
	// On panic path: always End(nil)
	// On normal path:
	//   0 returns: End(nil)
	//   1 return:  End(_sfRet0)          — matches JS/TS format exactly
	//   2+ returns: End(map[string]interface{}{"0": _sfRet0, "1": _sfRet1, ...})
	var normalEndArg ast.Expr
	switch len(returnVarNames) {
	case 0:
		normalEndArg = ast.NewIdent("nil")
	case 1:
		normalEndArg = ast.NewIdent(returnVarNames[0])
	default:
		normalEndArg = buildReturnValueMapExpr(returnVarNames)
	}

	// Build: defer func() {
	//     if r := recover(); r != nil {
	//         sfveritas.TransmitPanicWithLocals(_sfSpan.Context(), r, map[string]interface{}{...})
	//         _sfSpan.End(nil)
	//         panic(r) // re-panic
	//     }
	//     _sfSpan.End(map[string]interface{}{"0": _sfRet0, ...})
	// }()
	body := &ast.BlockStmt{
		List: []ast.Stmt{
			// if r := recover(); r != nil {
			&ast.IfStmt{
				Init: &ast.AssignStmt{
					Lhs: []ast.Expr{ast.NewIdent("_sfR")},
					Tok: token.DEFINE,
					Rhs: []ast.Expr{
						&ast.CallExpr{
							Fun: ast.NewIdent("recover"),
						},
					},
				},
				Cond: &ast.BinaryExpr{
					X:  ast.NewIdent("_sfR"),
					Op: token.NEQ,
					Y:  ast.NewIdent("nil"),
				},
				Body: &ast.BlockStmt{
					List: []ast.Stmt{
						// sfveritas.TransmitPanicWithLocals(ctx, r, locals)
						&ast.ExprStmt{
							X: &ast.CallExpr{
								Fun: &ast.SelectorExpr{
									X:   ast.NewIdent("sfveritas"),
									Sel: ast.NewIdent("TransmitPanicWithLocals"),
								},
								Args: []ast.Expr{
									&ast.CallExpr{
										Fun: &ast.SelectorExpr{
											X:   ast.NewIdent("_sfSpan"),
											Sel: ast.NewIdent("Context"),
										},
									},
									ast.NewIdent("_sfR"),
									localsMapExpr,
								},
							},
						},
						// _sfSpan.End(nil)
						&ast.ExprStmt{
							X: &ast.CallExpr{
								Fun: &ast.SelectorExpr{
									X:   ast.NewIdent("_sfSpan"),
									Sel: ast.NewIdent("End"),
								},
								Args: []ast.Expr{ast.NewIdent("nil")},
							},
						},
						// panic(_sfR)
						&ast.ExprStmt{
							X: &ast.CallExpr{
								Fun: ast.NewIdent("panic"),
								Args: []ast.Expr{
									ast.NewIdent("_sfR"),
								},
							},
						},
					},
				},
			},
			// _sfSpan.End(returnValueMap) — normal exit path
			&ast.ExprStmt{
				X: &ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   ast.NewIdent("_sfSpan"),
						Sel: ast.NewIdent("End"),
					},
					Args: []ast.Expr{normalEndArg},
				},
			},
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

// parseStmt parses a single Go statement from a string.
func parseStmt(src string) ast.Stmt {
	// Wrap in a function to make it a valid Go file
	wrapped := "package p\nfunc f() {\n" + src + "\n}"
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "", wrapped, 0)
	if err != nil {
		// Fallback: return an empty statement
		return &ast.EmptyStmt{}
	}
	fn := node.Decls[0].(*ast.FuncDecl)
	return fn.Body.List[0]
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
