package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseBasic(t *testing.T) {
	data := []byte(`
name: CI
on:
  push:
    branches: [main]
  pull_request:
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    concurrency:
      group: build-${{ github.ref }}
    steps:
      - run: echo hi
  deploy:
    runs-on: ubuntu-latest
    needs: build
    steps:
      - run: echo deploy
`)
	wf, err := Parse(".github/workflows/ci.yml", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if wf.Name != "CI" {
		t.Errorf("Name = %q, want CI", wf.Name)
	}
	if wf.ResolvedName() != "CI" {
		t.Errorf("ResolvedName() = %q, want CI", wf.ResolvedName())
	}
	for _, e := range []string{"push", "pull_request"} {
		if !wf.Events[e] {
			t.Errorf("expected event %q to be present, got %v", e, wf.Events)
		}
	}
	if wf.Concurrency == nil {
		t.Fatal("expected workflow-level concurrency")
	}
	if wf.Concurrency.GroupRaw != "${{ github.workflow }}-${{ github.ref }}" {
		t.Errorf("GroupRaw = %q", wf.Concurrency.GroupRaw)
	}
	if wf.Concurrency.CancelInProgress == nil || !*wf.Concurrency.CancelInProgress {
		t.Errorf("CancelInProgress = %v, want true", wf.Concurrency.CancelInProgress)
	}
	if len(wf.Jobs) != 2 {
		t.Fatalf("len(Jobs) = %d, want 2", len(wf.Jobs))
	}
	build := findJob(t, wf, "build")
	if build.Concurrency == nil || build.Concurrency.GroupRaw != "build-${{ github.ref }}" {
		t.Errorf("build job concurrency = %+v", build.Concurrency)
	}
	deploy := findJob(t, wf, "deploy")
	if deploy.Concurrency != nil {
		t.Errorf("deploy job should have no concurrency block, got %+v", deploy.Concurrency)
	}
}

func TestResolvedNameFallsBackToPath(t *testing.T) {
	data := []byte(`
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`)
	wf, err := Parse(".github/workflows/no-name.yml", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if wf.Name != "" {
		t.Errorf("Name = %q, want empty", wf.Name)
	}
	// Per GitHub's docs: github.workflow falls back to the workflow file's
	// path when there is no name: field.
	if wf.ResolvedName() != ".github/workflows/no-name.yml" {
		t.Errorf("ResolvedName() = %q, want the file path", wf.ResolvedName())
	}
}

func TestOnAsListAndString(t *testing.T) {
	list, err := Parse("x.yml", []byte("on: [push, pull_request]\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !list.Events["push"] || !list.Events["pull_request"] {
		t.Errorf("events = %v", list.Events)
	}

	scalar, err := Parse("y.yml", []byte("on: workflow_dispatch\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !scalar.Events["workflow_dispatch"] || len(scalar.Events) != 1 {
		t.Errorf("events = %v", scalar.Events)
	}
}

func TestConcurrencyAsBareString(t *testing.T) {
	wf, err := Parse("z.yml", []byte("on: push\nconcurrency: my-group\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if wf.Concurrency == nil || wf.Concurrency.GroupRaw != "my-group" {
		t.Fatalf("Concurrency = %+v", wf.Concurrency)
	}
	if wf.Concurrency.CancelInProgress == nil || *wf.Concurrency.CancelInProgress {
		t.Errorf("CancelInProgress should default to false, got %v", wf.Concurrency.CancelInProgress)
	}
}

func TestConcurrencyMissingGroupIsError(t *testing.T) {
	_, err := Parse("bad.yml", []byte("on: push\nconcurrency:\n  cancel-in-progress: true\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n"))
	if err == nil {
		t.Fatal("expected an error for a concurrency block with no group")
	}
}

func TestInvalidYAMLIsError(t *testing.T) {
	_, err := Parse("bad.yml", []byte("this: [is not valid yaml"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestUsesField(t *testing.T) {
	wf, err := Parse("caller.yml", []byte(`
on: push
jobs:
  call-it:
    uses: ./.github/workflows/reusable.yml
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	job := findJob(t, wf, "call-it")
	if job.Uses != "./.github/workflows/reusable.yml" {
		t.Errorf("Uses = %q", job.Uses)
	}
}

func TestLoadDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"a.yml":     "name: A\non: push\njobs:\n  x:\n    runs-on: ubuntu-latest\n    steps: []\n",
		"b.yaml":    "name: B\non: push\njobs:\n  x:\n    runs-on: ubuntu-latest\n    steps: []\n",
		"notes.txt": "not a workflow",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wfs, errs := LoadDir(root)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(wfs) != 2 {
		t.Fatalf("len(wfs) = %d, want 2 (txt file must be skipped)", len(wfs))
	}
}

func TestLoadDirMissingDirIsError(t *testing.T) {
	_, errs := LoadDir(t.TempDir())
	if len(errs) == 0 {
		t.Fatal("expected an error for a repo with no .github/workflows directory")
	}
}

func findJob(t *testing.T, wf *Workflow, id string) *Job {
	t.Helper()
	for _, j := range wf.Jobs {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("job %q not found", id)
	return nil
}
