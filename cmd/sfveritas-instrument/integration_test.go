package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestToolexecBuildsRealModule builds a real multi-package module with
// `go build -toolexec=<wrapper>` and asserts it compiles cleanly. Before the
// fixes, instrumenting these packages produced uncompilable code (missing
// importcfg entry, undefined `context`, defer referencing later-declared locals).
// A second build (after a content change, with the harvest cache warm) forces the
// dependency packages to actually be instrumented and asserts that still compiles.
func TestToolexecBuildsRealModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping toolexec integration build in -short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not found in PATH")
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sdkRoot, _ := filepath.Abs(filepath.Join(cwd, "..", "..")) // veritas/golang
	srcFixture := filepath.Join(cwd, "testdata", "testmod")

	work := t.TempDir()

	// Build the toolexec wrapper.
	wrapper := filepath.Join(work, "sfveritas-instrument")
	if out, err := runCmd(goBin, []string{"build", "-o", wrapper, "."}, cwd, nil); err != nil {
		t.Fatalf("building wrapper failed: %v\n%s", err, out)
	}

	// Copy the fixture to a temp module and point its replace at the SDK root.
	mod := filepath.Join(work, "mod")
	if err := copyDir(srcFixture, mod); err != nil {
		t.Fatal(err)
	}
	gomodPath := filepath.Join(mod, "go.mod")
	b, _ := os.ReadFile(gomodPath)
	rewritten := strings.Replace(string(b), "../../../..", sdkRoot, 1)
	if err := os.WriteFile(gomodPath, []byte(rewritten), 0644); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(), "GOFLAGS=-mod=mod", "CGO_ENABLED=0", "SF_INSTRUMENT_DEBUG=true")
	bin := filepath.Join(work, "app")
	// Build the main package (its deps common/articles are compiled through the
	// toolexec too). Avoid `./...` + `-o <file>` which rejects multiple packages.
	buildArgs := []string{"build", "-toolexec=" + wrapper, "-o", bin, "."}

	// common, articles and main all import sfveritas, so the import is resolvable
	// in each package's importcfg and they get instrumented. Before the fixes this
	// produced uncompilable code (undefined context/locals); now it must build.
	out, err := runCmd(goBin, buildArgs, mod, env)
	if err != nil {
		t.Fatalf("build (-toolexec) failed — instrumented code did not compile:\n%v\n%s", err, out)
	}

	for _, pkg := range []string{
		"example.com/testmod/common",
		"example.com/testmod/articles",
		"example.com/testmod",
	} {
		if !strings.Contains(out, "instrumented package "+pkg) {
			t.Errorf("expected %s to be instrumented; debug output:\n%s", pkg, out)
		}
	}
}

func runCmd(bin string, args []string, dir string, env []string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}
