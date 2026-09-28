package detect

import (
	"strings"
	"testing"

	"github.com/__GH_OWNER__/concurclash/internal/workflow"
)

func mustParse(t *testing.T, path, yaml string) *workflow.Workflow {
	t.Helper()
	wf, err := workflow.Parse(path, []byte(yaml))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return wf
}

func hasKind(findings []Finding, k Kind) bool {
	for _, f := range findings {
		if f.Kind == k {
			return true
		}
	}
	return false
}

// --- Negative control: a clean multi-workflow repo ---

func TestCleanRepoHasNoFindings(t *testing.T) {
	a := mustParse(t, ".github/workflows/lint.yml", `
name: Lint
on: [push, pull_request]
concurrency:
  group: lint-${{ github.ref }}
  cancel-in-progress: true
jobs:
  lint:
    runs-on: ubuntu-latest
    steps: [{run: echo lint}]
`)
	b := mustParse(t, ".github/workflows/test.yml", `
name: Test
on: [push, pull_request]
concurrency:
  group: test-${{ github.ref }}
  cancel-in-progress: true
jobs:
  test:
    runs-on: ubuntu-latest
    steps: [{run: echo test}]
`)
	c := mustParse(t, ".github/workflows/deploy.yml", `
name: Deploy
on:
  workflow_dispatch:
concurrency:
  group: deploy-${{ github.run_id }}
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps: [{run: echo deploy}]
`)
	findings := Analyze([]*workflow.Workflow{a, b, c})
	if len(findings) != 0 {
		t.Fatalf("expected no findings on a clean repo, got %+v", findings)
	}
}

// --- Definite collision: identical literal groups, no ref dependency ---

func TestDefiniteCollisionLiteralGroups(t *testing.T) {
	a := mustParse(t, ".github/workflows/one.yml", `
name: One
on: push
concurrency:
  group: prod-deploy
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps: []
`)
	b := mustParse(t, ".github/workflows/two.yml", `
name: Two
on: push
concurrency:
  group: prod-deploy
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps: []
`)
	findings := Analyze([]*workflow.Workflow{a, b})
	if !hasKind(findings, KindDefiniteCollision) {
		t.Fatalf("expected a definite collision, got %+v", findings)
	}
	for _, f := range findings {
		if f.Kind == KindDefiniteCollision && f.Severity != SeverityError {
			t.Errorf("definite collision should be error severity, got %s", f.Severity)
		}
	}
}

// --- Possible collision: group depends on workflow_dispatch inputs ---

func TestPossibleCollisionOnInputs(t *testing.T) {
	a := mustParse(t, ".github/workflows/one.yml", `
name: One
on: workflow_dispatch
concurrency:
  group: env-${{ inputs.environment }}
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps: []
`)
	b := mustParse(t, ".github/workflows/two.yml", `
name: Two
on: workflow_dispatch
concurrency:
  group: env-${{ inputs.environment }}
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps: []
`)
	findings := Analyze([]*workflow.Workflow{a, b})
	if !hasKind(findings, KindPossibleCollision) {
		t.Fatalf("expected a possible collision, got %+v", findings)
	}
	if hasKind(findings, KindDefiniteCollision) {
		t.Fatalf("inputs-dependent groups must not be reported as definite, got %+v", findings)
	}
}

// --- The docs' own recommended pattern collides when two workflows share a name ---

func TestDuplicateWorkflowNameFeedingGithubWorkflow(t *testing.T) {
	a := mustParse(t, ".github/workflows/ci.yml", `
name: CI
on: [push, pull_request]
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
`)
	b := mustParse(t, ".github/workflows/ci2.yml", `
name: CI
on: [push, pull_request]
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
jobs:
  test:
    runs-on: ubuntu-latest
    steps: []
`)
	findings := Analyze([]*workflow.Workflow{a, b})
	if !hasKind(findings, KindDuplicateWorkflowName) {
		t.Fatalf("expected a duplicate-workflow-name finding, got %+v", findings)
	}
	for _, f := range findings {
		if f.Kind == KindDuplicateWorkflowName && f.Severity != SeverityError {
			t.Errorf("duplicate workflow name should be error severity, got %s", f.Severity)
		}
	}
}

// Negative control: same duplicate name, but neither workflow's concurrency
// group actually references github.workflow — must not fire.
func TestDuplicateWorkflowNameQuietWhenUnused(t *testing.T) {
	a := mustParse(t, ".github/workflows/ci.yml", `
name: CI
on: push
concurrency:
  group: build-${{ github.ref }}
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
`)
	b := mustParse(t, ".github/workflows/ci2.yml", `
name: CI
on: push
concurrency:
  group: test-${{ github.ref }}
jobs:
  test:
    runs-on: ubuntu-latest
    steps: []
`)
	findings := Analyze([]*workflow.Workflow{a, b})
	if hasKind(findings, KindDuplicateWorkflowName) {
		t.Fatalf("did not expect duplicate-workflow-name finding when github.workflow is unused, got %+v", findings)
	}
}

// --- Job-level group identical to the workflow-level group ---

func TestSameRunNestingFlagged(t *testing.T) {
	a := mustParse(t, ".github/workflows/one.yml", `
name: One
on: push
concurrency:
  group: release-${{ github.ref }}
jobs:
  build:
    runs-on: ubuntu-latest
    concurrency:
      group: release-${{ github.ref }}
    steps: []
`)
	findings := Analyze([]*workflow.Workflow{a})
	if !hasKind(findings, KindSameGroupNesting) {
		t.Fatalf("expected a same-group nesting finding, got %+v", findings)
	}
}

// Negative control: a job group that differs from the workflow group (e.g.
// scoped per matrix cell) must not be flagged as nesting.
func TestDifferentJobGroupNotFlaggedAsNesting(t *testing.T) {
	a := mustParse(t, ".github/workflows/one.yml", `
name: One
on: push
concurrency:
  group: release-${{ github.ref }}
jobs:
  build:
    runs-on: ubuntu-latest
    concurrency:
      group: release-${{ github.ref }}-${{ matrix.os }}
    steps: []
`)
	findings := Analyze([]*workflow.Workflow{a})
	if hasKind(findings, KindSameGroupNesting) {
		t.Fatalf("did not expect nesting finding for a differently scoped job group, got %+v", findings)
	}
}

// --- cancel-in-progress mismatch on a colliding pair ---

func TestCancelInProgressMismatchReported(t *testing.T) {
	a := mustParse(t, ".github/workflows/one.yml", `
name: One
on: push
concurrency:
  group: prod-deploy
  cancel-in-progress: true
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps: []
`)
	b := mustParse(t, ".github/workflows/two.yml", `
name: Two
on: push
concurrency:
  group: prod-deploy
  cancel-in-progress: false
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps: []
`)
	findings := Analyze([]*workflow.Workflow{a, b})
	if !hasKind(findings, KindCancelMismatch) {
		t.Fatalf("expected a cancel-in-progress mismatch finding, got %+v", findings)
	}
	for _, f := range findings {
		if f.Kind == KindCancelMismatch && f.Severity != SeverityInfo {
			t.Errorf("cancel-in-progress mismatch should be info severity, got %s", f.Severity)
		}
	}
}

// --- github.run_id guarantees no collision even with an identical prefix ---

func TestRunIDNeverCollides(t *testing.T) {
	a := mustParse(t, ".github/workflows/one.yml", `
name: One
on: push
concurrency:
  group: deploy-${{ github.run_id }}
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps: []
`)
	b := mustParse(t, ".github/workflows/two.yml", `
name: Two
on: push
concurrency:
  group: deploy-${{ github.run_id }}
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps: []
`)
	findings := Analyze([]*workflow.Workflow{a, b})
	if len(findings) != 0 {
		t.Fatalf("expected no findings when groups are scoped by run_id, got %+v", findings)
	}
}

// --- Disjoint trigger ref-space: push-only vs pull_request-only sharing
// the same ref-dependent symbol text must not collide, since push refs
// (refs/heads/*) and PR merge refs (refs/pull/N/merge) can never be equal.

func TestDisjointTriggersNoCollision(t *testing.T) {
	a := mustParse(t, ".github/workflows/one.yml", `
name: One
on: push
concurrency:
  group: build-${{ github.ref }}
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
`)
	b := mustParse(t, ".github/workflows/two.yml", `
name: Two
on: pull_request
concurrency:
  group: build-${{ github.ref }}
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
`)
	findings := Analyze([]*workflow.Workflow{a, b})
	if len(findings) != 0 {
		t.Fatalf("expected no findings across disjoint ref-spaces, got %+v", findings)
	}
}

// --- Reusable workflow: a job-level group inside a called workflow is
// attributed to the caller and can collide with a sibling workflow.

func TestReusableWorkflowCollision(t *testing.T) {
	reusable := mustParse(t, ".github/workflows/reusable.yml", `
name: Reusable
on:
  workflow_call:
jobs:
  do-it:
    runs-on: ubuntu-latest
    concurrency:
      group: shared-lock
    steps: []
`)
	caller := mustParse(t, ".github/workflows/caller.yml", `
name: Caller
on: push
jobs:
  call-reusable:
    uses: ./.github/workflows/reusable.yml
`)
	other := mustParse(t, ".github/workflows/other.yml", `
name: Other
on: push
concurrency:
  group: shared-lock
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
`)
	findings := Analyze([]*workflow.Workflow{reusable, caller, other})
	if !hasKind(findings, KindDefiniteCollision) {
		t.Fatalf("expected the reusable workflow's job group to collide with other.yml, got %+v", findings)
	}
	found := false
	for _, f := range findings {
		if f.Kind != KindDefiniteCollision {
			continue
		}
		for _, l := range f.Locations {
			if l.Path == ".github/workflows/caller.yml" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected the collision to be attributed to caller.yml, got %+v", findings)
	}
}

// --- JSON/text sanity: Analyze results are deterministic across runs ---

func TestAnalyzeIsDeterministic(t *testing.T) {
	a := mustParse(t, ".github/workflows/one.yml", "name: One\non: push\nconcurrency:\n  group: g\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n")
	b := mustParse(t, ".github/workflows/two.yml", "name: Two\non: push\nconcurrency:\n  group: g\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n")
	f1 := Analyze([]*workflow.Workflow{a, b})
	f2 := Analyze([]*workflow.Workflow{b, a})
	if len(f1) != len(f2) {
		t.Fatalf("nondeterministic finding count: %d vs %d", len(f1), len(f2))
	}
	for i := range f1 {
		if f1[i].Kind != f2[i].Kind || f1[i].Message != f2[i].Message {
			t.Errorf("finding %d differs by input order:\n%+v\n%+v", i, f1[i], f2[i])
		}
	}
}

func TestMessagesDoNotPanicOnEmptyInput(t *testing.T) {
	findings := Analyze(nil)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for an empty workflow set, got %+v", findings)
	}
}

// Sanity check that the rendered message names both files for readability.
func TestCollisionMessageNamesBothFiles(t *testing.T) {
	a := mustParse(t, ".github/workflows/one.yml", "name: One\non: push\nconcurrency:\n  group: g\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n")
	b := mustParse(t, ".github/workflows/two.yml", "name: Two\non: push\nconcurrency:\n  group: g\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: []\n")
	findings := Analyze([]*workflow.Workflow{a, b})
	var msg string
	for _, f := range findings {
		if f.Kind == KindDefiniteCollision {
			msg = f.Message
		}
	}
	if !strings.Contains(msg, "one.yml") || !strings.Contains(msg, "two.yml") {
		t.Errorf("message %q does not name both files", msg)
	}
}
