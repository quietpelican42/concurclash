package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkflow(t *testing.T, dir, name, content string) {
	t.Helper()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunExitCodeCleanRepo(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", "name: CI\non: push\nconcurrency:\n  group: ci-${{ github.ref }}\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

// A literal group shared across workflows is downgraded to a warning (it's
// a common, deliberate pattern), but the CLI still exits 1: any finding,
// any severity, is a non-clean exit.
func TestRunExitCodeWithWarningOnlyFindings(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "a.yml", "name: A\non: push\nconcurrency:\n  group: shared\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n")
	writeWorkflow(t, dir, "b.yml", "name: B\non: push\nconcurrency:\n  group: shared\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps: []\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{dir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "WARNING") {
		t.Errorf("expected a warning-level finding in stdout, got %q", stdout.String())
	}
}

func TestRunExitCodeWithErrorFinding(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "a.yml", "name: CI\non: push\nconcurrency:\n  group: ${{ github.workflow }}-${{ github.ref }}\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n")
	writeWorkflow(t, dir, "b.yml", "name: CI\non: push\nconcurrency:\n  group: ${{ github.workflow }}-${{ github.ref }}\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps: []\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{dir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "ERROR") {
		t.Errorf("expected an error-level finding in stdout, got %q", stdout.String())
	}
}

func TestRunExitCodeMissingDir(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{t.TempDir()}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, stderr.String())
	}
}

func TestRunExitCodeInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "bad.yml", "this: [is not valid yaml")
	var stdout, stderr bytes.Buffer
	code := run([]string{dir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, stderr.String())
	}
}

func TestRunJSONFormat(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "a.yml", "name: A\non: push\nconcurrency:\n  group: shared\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n")
	writeWorkflow(t, dir, "b.yml", "name: B\non: push\nconcurrency:\n  group: shared\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps: []\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-format", "json", dir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "[") {
		t.Errorf("expected a JSON array, got %q", stdout.String())
	}
}

func TestRunUnknownFormatIsUsageError(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "a.yml", "name: A\non: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-format", "yaml", dir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}
