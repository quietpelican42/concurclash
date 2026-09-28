# concurclash

Finds GitHub Actions `concurrency:` group collisions across a repository's workflows.

```sh
concurclash .
```

```
ERROR: [duplicate-workflow-name] workflows .github/workflows/ci.yml, .github/workflows/ci2.yml all declare name: "CI", so github.workflow resolves to the same string in each; at least one of them uses github.workflow in a concurrency group, which GitHub's docs say must be unique across workflows
    .github/workflows/ci.yml:1  <->  .github/workflows/ci2.yml:1
WARNING: [possible-collision] .github/workflows/ci.yml and .github/workflows/ci2.yml can resolve to the same concurrency group ("CI-${{ github.ref }}" vs "CI-${{ github.ref }}")
    .github/workflows/ci.yml:7  <->  .github/workflows/ci2.yml:7

1 error(s), 1 warning(s), 0 info
```

## Why

GitHub's own docs state the rule plainly:

> concurrency group names must be unique across workflows to avoid canceling in-progress jobs or runs from other workflows. Otherwise, any previously in-progress or pending job will be canceled, regardless of the workflow.
> — [Using concurrency](https://docs.github.com/en/actions/using-jobs/using-concurrency)

Nothing enforces that rule at authoring time. Two workflows can independently
land on the same group string — most easily by both copying the docs' own
example pattern, `${{ github.workflow }}-${{ github.ref }}`, while also
happening to share a `name:`. `github.workflow` resolves to that `name:`
field (or the file path, if there is none — also documented), so two
identically named workflows produce identical `github.workflow` values, and
a run of one cancels or blocks behind a run of the other.

**Measured**: two workflows in one repo, both named `CI`, both using the
docs' own recommended group pattern:

```yaml
# .github/workflows/ci.yml and ci2.yml, both:
name: CI
on:
  push:
    branches: [main]
  pull_request:
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
```

```
$ actionlint
$ echo "exit: $?"
exit: 0
```

```
$ zizmor .github/workflows/
 INFO zizmor: 🌈 zizmor v1.30.1
 WARN audit: zizmor: zizmor is running in offline mode by default; ...
 INFO audit: zizmor: 🌈 completed .github/workflows/ci.yml
 INFO audit: zizmor: 🌈 completed .github/workflows/ci2.yml
warning[excessive-permissions]: overly broad permissions
  --> .github/workflows/ci.yml:10:3
  ... (same for ci2.yml; nothing about the shared concurrency group)
6 findings (4 suppressed): 0 informational, 0 low, 2 medium, 0 high
$ echo "exit: $?"
exit: 13
```

actionlint 1.7.12 checks the `concurrency:` block's shape (it does reject an
unrecognized key such as a mistyped `queue:`) but not whether two workflows'
groups can collide. zizmor 1.30.1 parses `concurrency:` for its own audits
(e.g. flagging a missing `cancel-in-progress` in some contexts) but has no
audit for cross-workflow group collisions either; on this fixture its only
finding is an unrelated `excessive-permissions` warning. Neither tool
mentioned `concurrency` at all when run against 193 real workflow files
across 8 large open-source repositories (below). concurclash is the only one
of the three that reports this pair.

## Installation

```sh
go install github.com/__GH_OWNER__/concurclash/cmd/concurclash@latest
```

Go >= 1.23. No other tools required.

## Usage

### Scan a repository

```sh
concurclash /path/to/repo   # defaults to the current directory
```

### In CI, as an annotation

```sh
concurclash -format github .
```

Emits `::error file=...,line=...::...` / `::warning ...` / `::notice ...`
lines that GitHub renders as inline check annotations.

### Machine-readable output

```sh
concurclash -format json . | jq '.[] | select(.severity == "error")'
```

## Reference

### Flags

| Name      | Type   | Default | Description                          |
| --------- | ------ | ------- | ------------------------------------ |
| `-format` | string | `text`  | Output format: `text`, `json`, `github` |

### Exit codes

| Code | Meaning                                                        |
| ---- | --------------------------------------------------------------- |
| `0`  | No findings.                                                     |
| `1`  | One or more findings (any severity).                             |
| `2`  | Could not analyse: unreadable path, no workflows found, invalid workflow YAML, or an unknown `-format`. |

### Finding kinds

| Kind                        | Severity | Meaning |
| ---------------------------- | -------- | ------- |
| `duplicate-workflow-name`     | error    | Two or more workflows declare the same `name:`, and at least one uses `github.workflow` inside a concurrency group. |
| `definite-collision`          | error    | Two groups are provably equal whenever both can run (pure literal text, or context values that are always equal within one repo). |
| `possible-collision`          | warning  | Two groups can be equal depending on runtime values concurclash cannot see (`inputs.*`, `matrix.*`, `github.ref`/`github.head_ref` when the two workflows' triggers may overlap). |
| `job-workflow-same-group`     | warning  | A job's own concurrency group is textually identical to its workflow's group. GitHub does not document what this does; flagged as an undocumented risk, not a confirmed deadlock. |
| `cancel-in-progress-mismatch` | info     | Attached to a collision finding when the two `cancel-in-progress` values differ (one run would be canceled, the other would queue). |

## How it works

1. Parse every `*.yml`/`*.yaml` under `.github/workflows`, plus jobs pulled
   in via `uses: ./.github/workflows/x.yml` (a locally reusable workflow's
   job-level groups are attributed to the calling workflow, since a called
   reusable workflow's jobs share the caller's run and ref context — see
   [reusing workflows](https://docs.github.com/en/actions/sharing-automations/reusing-workflows)).
2. Split each `concurrency.group` string on `${{ ... }}` boundaries into
   literal and symbolic parts, and substitute `github.workflow` with the
   owning workflow's resolved name (its `name:`, or its file path when
   absent — GitHub's own documented fallback).
3. Classify every other symbol: always equal within a repo
   (`github.repository`), shared context that depends on both runs sharing
   ref-space (`github.ref`, `github.head_ref`, ...), guaranteed unique per
   run (`github.run_id`, `github.run_number`, `github.run_attempt`), or
   caller-supplied and unknowable (`inputs.*`, `matrix.*`, `env.*`,
   `github.event.*`).
4. Compare every pair of groups structurally. A pair can only collide if
   every part lines up: identical literal text, or symbols that could hold
   the same value. `github.run_id`/`run_number`/`run_attempt` on either
   side rules a pair out entirely — a group scoped by run ID is unique by
   construction. A `github.ref`/`github.head_ref` match only counts when
   the two workflows' triggers can plausibly share ref-space (see
   Limitations).
5. Report duplicate workflow names, job-vs-own-workflow nesting, and
   `cancel-in-progress` mismatches on colliding pairs as separate finding
   kinds.

Parsing uses `gopkg.in/yaml.v3`, the one runtime dependency. The standard
library has no YAML parser, and this tool needs `yaml.Node`'s line numbers
(for annotation output) and its ability to distinguish a bare string
`concurrency:` block from a `{group, cancel-in-progress}` mapping without
hand-rolling a YAML tokenizer — writing that by hand to save one dependency
was judged not worth the risk of subtly misparsing real workflow files.

## Limitations

concurclash is a static, symbolic checker. It does not talk to the GitHub
API and does not execute anything.

- It does not model `branches`/`branches-ignore`/`paths`/`tags` filters
  under `on:`. Two workflows are treated as possibly sharing ref-space
  whenever their trigger *event names* overlap (e.g. both have `push`),
  even if one is scoped to a branch the other never touches. This is a
  source of false positives the tool accepts in exchange for not missing
  real ones.
- Trigger-overlap is a coarse three-way model: push/schedule/workflow_dispatch/
  release/workflow_call/create/delete are treated as one "branch-ref" class,
  pull_request/pull_request_target as a "PR-ref" class, and anything else
  (issue_comment, workflow_run, repository_dispatch, ...) as "unknown",
  which is conservatively assumed to overlap with everything. This is an
  approximation of the real ref-space rules, not a re-implementation of them.
- It cannot evaluate arbitrary GitHub Actions expressions (`format()`,
  `contains()`, ternaries, `fromJSON`, etc.) — anything other than a
  recognized context path is treated as an opaque, unknowable symbol and
  compared conservatively (never "definite", never ruled out either).
- `job-workflow-same-group` is flagged because GitHub's docs do not say
  what happens when a job's group equals its own workflow's group; this
  tool did not (and, without pushing real runs to GitHub, could not) verify
  actual runtime behavior. Treat it as "worth checking by hand," not a
  confirmed bug.
- Local reusable workflow calls (`uses: ./...`) are resolved; workflows
  reused from another repository or a version tag are not (their content is
  not available to a static scan of this repository alone).
- `pull_request_target`'s `github.ref` actually resolves to the *base*
  branch ref (not the PR merge ref used by `pull_request`), which would
  make it its own ref-space; this tool currently lumps it into the same
  "PR-ref" class as `pull_request` for simplicity, which can under- or
  over-approximate depending on the pair. Documented here rather than
  silently assumed.

## Compatibility

Built and tested with Go 1.27.1 (darwin/arm64), module directive `go 1.23`.
Not run on any other OS or Go version. CI runs the same checks on
`ubuntu-latest` with Go 1.23 via `actions/setup-go` — that is "runs in CI,"
not "tested here."

Checked against actionlint 1.7.12 and zizmor 1.30.1 (both via Homebrew on
macOS) as of this writing; their concurrency-related coverage may change in
future releases.

### Real-world noise floor

Ran against 8 large open-source repositories' `.github/workflows`
directories (shallow, sparse clones; 193 workflow files total):

| Repository | Workflow files | concurclash findings |
| --- | --- | --- |
| cli/cli | 14 | 0 |
| containerd/containerd | 17 | 0 |
| etcd-io/etcd | 11 | 0 |
| grafana/grafana | 97 | 1 warning |
| hashicorp/terraform | 11 | 0 |
| helm/helm | 8 | 0 |
| moby/moby | 20 | 0 |
| prometheus/prometheus | 15 | 0 |

The one finding: grafana/grafana's `create-release-tag.yml` is a reusable
workflow whose job-level group is `create-release-tag-${{ inputs.tag }}`; it
is both directly `workflow_dispatch`-able and called via
`uses: ./.github/workflows/create-release-tag.yml` from
`release-comms.yml`. A manual dispatch and the automated release path
tagging the same version at the same time would collide — a real, if
narrow, possible collision, correctly reported as a warning rather than an
error since it depends on runtime input equality. Across these same 193
files, `actionlint` and `zizmor` never mentioned `concurrency` in their
output at all (actionlint's only `concurrency`-related lines anywhere in
this run were unrelated schema errors about a `queue:` key GitHub added
after actionlint 1.7.12 shipped).

## Development

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
gofmt -l .   # must print nothing
```

40 test cases (including table-driven subtests) across 4 packages, all
passing at the time of writing.

## License

MIT. See [LICENSE](LICENSE).
