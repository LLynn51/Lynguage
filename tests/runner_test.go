package golden_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

var (
	updateGoldens  = flag.Bool("update", false, "refresh pass/*.out golden snapshots")
	repositoryRoot string
	lynguageCLI    string
)

func TestMain(m *testing.M) {
	flag.Parse()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "cannot locate golden test runner")
		os.Exit(1)
	}
	repositoryRoot = filepath.Dir(filepath.Dir(filename))

	tempDir, err := os.MkdirTemp("", "lynguage-golden-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temporary build directory: %v\n", err)
		os.Exit(1)
	}

	executable := "lynguage"
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	lynguageCLI = filepath.Join(tempDir, executable)
	build := exec.Command("go", "build", "-o", lynguageCLI, "./cmd/lynguage")
	build.Dir = repositoryRoot
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		fmt.Fprintf(os.Stderr, "build Lynguage CLI: %v\n%s", buildErr, output)
		os.Exit(1)
	}

	code := m.Run()
	if err := os.RemoveAll(tempDir); err != nil {
		fmt.Fprintf(os.Stderr, "remove temporary build directory: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func TestPassGoldens(t *testing.T) {
	for _, sourcePath := range sourceFiles(t, "pass") {
		sourcePath := sourcePath
		name := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
		t.Run(name, func(t *testing.T) {
			vm := runCLI(t, sourcePath, false)
			native := runCLI(t, sourcePath, true)
			if vm.exitCode != 0 {
				t.Fatalf("VM exited with %d\nstderr:\n%s", vm.exitCode, vm.stderr)
			}
			if native.exitCode != 0 {
				t.Fatalf("transpiled program exited with %d\nstderr:\n%s", native.exitCode, native.stderr)
			}
			if vm.stderr != "" {
				t.Errorf("VM wrote to stderr:\n%s", vm.stderr)
			}
			if native.stderr != "" {
				t.Errorf("transpiled program wrote to stderr:\n%s", native.stderr)
			}
			if vm.stdout != native.stdout {
				t.Fatalf("backends produced different output\nVM:\n%s\ntranspiled:\n%s", vm.stdout, native.stdout)
			}

			goldenPath := strings.TrimSuffix(sourcePath, filepath.Ext(sourcePath)) + ".out"
			if *updateGoldens {
				if err := os.WriteFile(goldenPath, []byte(vm.stdout), 0o644); err != nil {
					t.Fatalf("update %s: %v", goldenPath, err)
				}
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden snapshot %s: %v", goldenPath, err)
			}
			if !bytes.Equal([]byte(vm.stdout), want) {
				t.Errorf("stdout differs from %s\nwant:\n%s\ngot:\n%s", goldenPath, want, vm.stdout)
			}
		})
	}
}

func TestFailGoldens(t *testing.T) {
	for _, sourcePath := range sourceFiles(t, "fail") {
		sourcePath := sourcePath
		name := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
		t.Run(name, func(t *testing.T) {
			errPath := strings.TrimSuffix(sourcePath, filepath.Ext(sourcePath)) + ".err"
			expectedBytes, err := os.ReadFile(errPath)
			if err != nil {
				t.Fatalf("read expected error %s: %v", errPath, err)
			}
			expected := strings.TrimSpace(string(expectedBytes))
			if expected == "" {
				t.Fatalf("%s must contain a non-empty error substring", errPath)
			}

			for _, mode := range []struct {
				name      string
				transpile bool
			}{{name: "vm"}, {name: "transpile", transpile: true}} {
				mode := mode
				t.Run(mode.name, func(t *testing.T) {
					result := runCLI(t, sourcePath, mode.transpile)
					if result.exitCode == 0 {
						t.Fatalf("command unexpectedly succeeded\nstdout:\n%s", result.stdout)
					}
					if !strings.Contains(result.stderr, expected) {
						t.Errorf("stderr does not contain %q\nstderr:\n%s", expected, result.stderr)
					}
				})
			}
		})
	}
}

func sourceFiles(t *testing.T, category string) []string {
	t.Helper()
	pattern := filepath.Join(repositoryRoot, "tests", category, "*.lyn")
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("scan %s: %v", pattern, err)
	}
	if len(files) == 0 {
		t.Fatalf("no golden test sources matched %s", pattern)
	}
	sort.Strings(files)
	return files
}

type commandResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func runCLI(t *testing.T, sourcePath string, transpile bool) commandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{"run"}
	if transpile {
		args = append(args, "--transpile")
	}
	args = append(args, sourcePath)
	command := exec.CommandContext(ctx, lynguageCLI, args...)
	command.Dir = repositoryRoot
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("command timed out after 30s: %s %s", lynguageCLI, strings.Join(args, " "))
	}

	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("start command: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}
