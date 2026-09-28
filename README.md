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

> If you have multiple workflows in the same repository, concurrency group names must be unique across workflows to avoid canceling in-progress jobs or runs from other workflows.
> — [Using concurrency](https://docs.github.com/en/actions/using-jobs/using-concurrency)

Nothing enforces that rule at authoring time. Two workflows can independently
land on the same group string — most easily by both copying the docs' own
example pattern, `${{ github.workflow }}-${{ github.ref }}`, while also
happening to share a `name:`. `github.workflow` resolves to that `name:`
field (or the file path, if there is none — also documented), so two
identically named workflows produce identical `github.workflow` values, and
a run of one cancels or blocks behind a run of the other.

A second, separate hazard applies even without any of that: a plain string
group deliberately shared between two workflows (a common pattern, e.g. one
lock name to serialize deploys to an environment) is still subject to what
the docs say happens inside one group, regardless of `cancel-in-progress`:

> By default, any existing `pending` job or workflow in the same concurrency group will be canceled and the new queued job or workflow will take its place.
> At most one job or workflow run can be `pending` in the concurrency group.
> — [Using concurrency](https://docs.github.com/en/actions/using-jobs/using-concurrency)

So a queued run of `deploy-api` can be silently replaced by a queued run of
`deploy-web` if they share a group, even with `cancel-in-progress: false` on
both. concurclash treats these two hazards differently: a collision that
only exists because of an expression (`github.workflow`, a duplicate `name:`,
etc.) is reported as an error, since it usually is not what the author
intended; a collision between two groups written as plain strings is
reported as a warning, since sharing a literal name on purpose is common —
see `literal-shared-group` below, and how to acknowledge it.

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

### Suppressing an intentional collision

A `literal-shared-group` finding — two workflows written with the exact same
plain-string `concurrency.group`, no `${{ ... }}` in either — is often
deliberate (a shared lock name to serialize deploys). Acknowledge it with a
trailing comment on the `group:` line:

```yaml
concurrency:
  group: production-deploy  # concurclash: shared
  cancel-in-progress: false
```

The comment can go on either of the two colliding groups; concurclash then
drops that pair's finding entirely (including any `cancel-in-progress`
mismatch note for the same pair). It does not suppress `duplicate-workflow-name`,
`definite-collision`, `possible-collision`, or `job-workflow-same-group`
findings — those indicate the group is equal because of an expression or a
naming accident, not a plain string someone chose on purpose, so there is
nothing here to acknowledge away.

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
| `definite-collision`          | error    | Two groups are provably equal whenever both can run, and at least one side's group contains an expression (`${{ ... }}`) — e.g. both key off `github.repository`, which is always the same value in one repo. Excludes plain-string-vs-plain-string matches; see `literal-shared-group`. |
| `literal-shared-group`        | warning  | Two groups are provably equal, and **neither** side's group contains any expression at all — a plain string shared on purpose is common (e.g. one lock name to serialize deploys). Suppressible; see "Suppressing an intentional collision". |
| `possible-collision`          | warning  | Two groups can be equal depending on runtime values concurclash cannot see (`inputs.*`, `matrix.*`, `github.ref`/`github.head_ref` when the two workflows' triggers may overlap). |
| `job-workflow-same-group`     | warning  | A job's own concurrency group is textually identical to its workflow's group. GitHub does not document what this does; flagged as an undocumented risk, not a confirmed deadlock. |
| `cancel-in-progress-mismatch` | info     | Attached to a collision finding when the two `cancel-in-progress` values differ. Per GitHub's docs this doesn't just mean "one cancels, one doesn't": a *pending* run is replaced by a newer pending run in the same group regardless of either side's `cancel-in-progress` setting — only a currently *running* job's cancellation depends on it. |

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
5. When a pair is provably equal, decide whether it looks accidental or
   deliberate: if either side's *authored* group (before any substitution)
   contains an expression, it's `definite-collision` (error); if neither
   side has any expression at all — a plain string typed the same way in
   two files — it's `literal-shared-group` (warning), and is dropped
   entirely if either side carries a `# concurclash: shared` comment on its
   `group:` line.
6. Report duplicate workflow names, job-vs-own-workflow nesting, and
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
- The error/warning split between `definite-collision` and
  `literal-shared-group` is a heuristic, not a certainty: "the authored
  group contains an expression" is a proxy for "probably accidental," and
  a proxy can be wrong in both directions. A group like
  `${{ 'production-deploy' }}` (an expression that only ever evaluates to
  one literal string) is treated as `definite-collision` even though it's
  functionally identical to writing the string directly; conversely, two
  independently-chosen plain strings that happen to match are still
  `literal-shared-group` even when nobody intended to share anything. Use
  `# concurclash: shared` to acknowledge the latter case; there is
  currently no equivalent for suppressing a `definite-collision`.

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

Re-ran against the same 8 clones after adding the `literal-shared-group`
downgrade: the result is unchanged (this one finding was already
`possible-collision`, driven by `inputs.tag`, not a plain-string match), and
none of the 193 files triggered `literal-shared-group` either.

## Development

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
gofmt -l .   # must print nothing
```

44 test cases (including table-driven subtests) across 4 packages, all
passing at the time of writing.

## License

MIT. See [LICENSE](LICENSE).
