// Package detect finds concurrency group collisions across a set of parsed
// GitHub Actions workflows: this is the core analysis concurclash performs.
package detect

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/quietpelican42/concurclash/internal/expr"
	"github.com/quietpelican42/concurclash/internal/workflow"
)

// Severity is how confidently and how urgently a Finding should be treated.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Kind identifies which check produced a Finding.
type Kind string

const (
	KindDefiniteCollision     Kind = "definite-collision"
	KindPossibleCollision     Kind = "possible-collision"
	KindDuplicateWorkflowName Kind = "duplicate-workflow-name"
	KindSameGroupNesting      Kind = "job-workflow-same-group"
	KindCancelMismatch        Kind = "cancel-in-progress-mismatch"
	// KindLiteralSharedGroup is a definite collision where neither side's
	// authored group contains any "${{ ... }}" expression at all — a group
	// typed as a plain string and shared across workflows on purpose is a
	// common, deliberate pattern (serializing deploys to one environment),
	// so this is reported as a warning rather than an error, and can be
	// acknowledged with a "# concurclash: shared" comment on the group
	// line to suppress it entirely. See the README.
	KindLiteralSharedGroup Kind = "literal-shared-group"
)

// Location pins a Finding to one workflow file, and optionally one job
// inside it.
type Location struct {
	Path  string `json:"path"`
	JobID string `json:"job,omitempty"`
	Line  int    `json:"line,omitempty"`
	Group string `json:"group,omitempty"`
}

// Finding is one thing concurclash reports.
type Finding struct {
	Kind      Kind       `json:"kind"`
	Severity  Severity   `json:"severity"`
	Message   string     `json:"message"`
	Locations []Location `json:"locations"`
}

// unit is one concurrency block found in the workflow set, already
// resolved for reusable-workflow calls: a job-level block inside a
// workflow reached via "uses: ./.github/workflows/x.yml" is attributed to
// the calling workflow's trigger, because a called reusable workflow's
// jobs run inside the caller's own run and share its ref/event context.
type unit struct {
	wfPath     string // workflow file that OWNS the trigger context (the caller, for reused jobs)
	jobID      string // "" for a workflow-level block
	origin     string // e.g. "" or "via ./.github/workflows/reusable.yml"
	events     map[string]bool
	rawGroup   string
	parts      []expr.Part // github.workflow already substituted
	usesGHWF   bool        // true if the pre-substitution group referenced github.workflow
	hasSymbol  bool        // true if the pre-substitution group contained ANY "${{ ... }}" expression
	suppressed bool        // true if the group line carries "# concurclash: shared"
	cip        *bool
	cipExpr    string
	line       int
}

type refClass int

const (
	refBranch refClass = iota
	refPR
	refOther
)

func classesFor(events map[string]bool) map[refClass]bool {
	out := map[refClass]bool{}
	if len(events) == 0 {
		// No modeled trigger (e.g. workflow_call callee with nothing else) —
		// conservative: could be anything.
		out[refOther] = true
		return out
	}
	for e := range events {
		switch e {
		case "pull_request", "pull_request_target":
			out[refPR] = true
		case "push", "schedule", "workflow_dispatch", "release", "workflow_call", "create", "delete":
			out[refBranch] = true
		default:
			out[refOther] = true
		}
	}
	return out
}

// mayOverlap reports whether two trigger event sets could plausibly put two
// runs in the same ref-space (both able to produce an equal github.ref /
// github.head_ref value at some point). refOther is treated as "unknown,
// assume yes" to avoid false negatives, at the cost of some noise — see the
// README's "What this approximates" section.
func mayOverlap(a, b map[string]bool) bool {
	ca, cb := classesFor(a), classesFor(b)
	if ca[refOther] || cb[refOther] {
		return true
	}
	for c := range ca {
		if cb[c] {
			return true
		}
	}
	return false
}

// verdict ranks how sure concurclash is that two groups can be equal.
type verdict int

const (
	vNone verdict = iota
	vPossible
	vDefinite
)

func min(v, to verdict) verdict {
	if to < v {
		return to
	}
	return v
}

// compareCrossRun compares two concurrency groups belonging to different
// workflow runs (possibly the same workflow triggered twice, or two
// different workflows). refOverlap says whether the two runs could share
// ref-space.
func compareCrossRun(a, b []expr.Part, refOverlap bool) verdict {
	if len(a) != len(b) {
		return vNone
	}
	v := vDefinite
	for i := range a {
		pa, pb := a[i], b[i]
		switch {
		case pa.Kind == expr.Literal && pb.Kind == expr.Literal:
			if pa.Text != pb.Text {
				return vNone
			}
		case pa.Kind == expr.Symbol && pb.Kind == expr.Symbol:
			if pa.Class == expr.RunUnique || pb.Class == expr.RunUnique {
				// A run-unique value (github.run_id/run_number/run_attempt)
				// is, by definition, never equal between two different runs.
				return vNone
			}
			if pa.Text != pb.Text {
				v = min(v, vPossible)
				continue
			}
			switch pa.Class {
			case expr.RepoConstant:
				// Always equal within one repository; no downgrade.
			case expr.RefShared:
				if !refOverlap {
					return vNone
				}
				v = min(v, vPossible)
			default: // Variable, Opaque
				v = min(v, vPossible)
			}
		default:
			// Literal vs Symbol: can't rule either way statically.
			v = min(v, vPossible)
		}
	}
	return v
}

// sameRunEqual compares a workflow-level group against a job-level group
// within the SAME workflow run: every context value (github.*, inputs.*,
// vars.*, env at workflow scope) is literally identical for both, because
// it is the same run. Only structural/textual identity decides equality.
func sameRunEqual(a, b []expr.Part) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].Text != b[i].Text {
			return false
		}
	}
	return true
}

func substituteGithubWorkflow(parts []expr.Part, resolvedName string) []expr.Part {
	out := make([]expr.Part, len(parts))
	for i, p := range parts {
		if p.Kind == expr.Symbol && expr.IsGithubWorkflow(p.Text) {
			out[i] = expr.Part{Kind: expr.Literal, Text: resolvedName}
			continue
		}
		out[i] = p
	}
	return out
}

func usesGithubWorkflow(parts []expr.Part) bool {
	for _, p := range parts {
		if p.Kind == expr.Symbol && expr.IsGithubWorkflow(p.Text) {
			return true
		}
	}
	return false
}

// hasAnySymbol reports whether the group, as the author wrote it, contains
// any "${{ ... }}" expression at all. A group with none is a plain string
// literal — sharing one across workflows is a common deliberate pattern
// (e.g. a single lock name to serialize deploys), as opposed to a group
// that computes to the same value only because of an expression like
// github.workflow. See KindLiteralSharedGroup.
func hasAnySymbol(parts []expr.Part) bool {
	for _, p := range parts {
		if p.Kind == expr.Symbol {
			return true
		}
	}
	return false
}

// resolveLocalUses turns a "uses:" value into a workflow-relative path if
// it references a local reusable workflow ("./.github/workflows/x.yml" or
// "./x.yml" relative to the workflows directory is NOT valid per GitHub —
// local reusable workflow refs are always repo-root-relative), or "" if it
// is not a local reference (a versioned/external "owner/repo/.github/...@ref").
func resolveLocalUses(uses string) string {
	u := strings.TrimSpace(uses)
	if !strings.HasPrefix(u, "./") {
		return ""
	}
	return path.Clean(u)
}

// Analyze runs every check over the given workflow set and returns all
// findings, sorted for stable output.
func Analyze(workflows []*workflow.Workflow) []Finding {
	// Work on a path-sorted copy so the result does not depend on the
	// order workflows were loaded in.
	workflows = append([]*workflow.Workflow(nil), workflows...)
	sort.Slice(workflows, func(i, j int) bool { return workflows[i].Path < workflows[j].Path })

	byPath := map[string]*workflow.Workflow{}
	for _, w := range workflows {
		byPath[w.Path] = w
	}

	units := collectUnits(workflows, byPath)
	sort.Slice(units, func(i, j int) bool {
		if units[i].wfPath != units[j].wfPath {
			return units[i].wfPath < units[j].wfPath
		}
		return units[i].jobID < units[j].jobID
	})

	var findings []Finding
	findings = append(findings, checkDuplicateNames(workflows, units)...)
	findings = append(findings, checkSameRunNesting(workflows)...)
	findings = append(findings, checkCrossRunCollisions(units)...)

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return severityRank(findings[i].Severity) < severityRank(findings[j].Severity)
		}
		li, lj := findings[i].Locations, findings[j].Locations
		if len(li) == 0 || len(lj) == 0 {
			return len(li) > len(lj)
		}
		if li[0].Path != lj[0].Path {
			return li[0].Path < lj[0].Path
		}
		return findings[i].Message < findings[j].Message
	})
	return findings
}

func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

// collectUnits gathers every workflow-level and job-level concurrency
// block, substituting github.workflow, and pulling in job-level blocks
// from reusable workflows called via a local "uses:", attributed to the
// calling workflow's trigger context. Recursion is bounded and
// cycle-guarded.
func collectUnits(workflows []*workflow.Workflow, byPath map[string]*workflow.Workflow) []unit {
	var units []unit
	for _, w := range workflows {
		name := w.ResolvedName()
		if w.Concurrency != nil {
			units = append(units, unit{
				wfPath:     w.Path,
				jobID:      "",
				events:     w.Events,
				rawGroup:   w.Concurrency.GroupRaw,
				parts:      substituteGithubWorkflow(w.Concurrency.GroupParts, name),
				usesGHWF:   usesGithubWorkflow(w.Concurrency.GroupParts),
				hasSymbol:  hasAnySymbol(w.Concurrency.GroupParts),
				suppressed: w.Concurrency.Suppressed,
				cip:        w.Concurrency.CancelInProgress,
				cipExpr:    w.Concurrency.CancelInProgressExpr,
				line:       w.Concurrency.Line,
			})
		}
		for _, j := range w.Jobs {
			if j.Concurrency != nil {
				units = append(units, unit{
					wfPath:     w.Path,
					jobID:      j.ID,
					events:     w.Events,
					rawGroup:   j.Concurrency.GroupRaw,
					parts:      substituteGithubWorkflow(j.Concurrency.GroupParts, name),
					usesGHWF:   usesGithubWorkflow(j.Concurrency.GroupParts),
					hasSymbol:  hasAnySymbol(j.Concurrency.GroupParts),
					suppressed: j.Concurrency.Suppressed,
					cip:        j.Concurrency.CancelInProgress,
					cipExpr:    j.Concurrency.CancelInProgressExpr,
					line:       j.Concurrency.Line,
				})
			}
			units = append(units, collectCalledUnits(w, j, byPath, map[string]bool{w.Path: true}, "")...)
		}
	}
	return units
}

func collectCalledUnits(caller *workflow.Workflow, job *workflow.Job, byPath map[string]*workflow.Workflow, visited map[string]bool, chain string) []unit {
	target := resolveLocalUses(job.Uses)
	if target == "" {
		return nil
	}
	called, ok := byPath[target]
	if !ok || visited[called.Path] {
		return nil
	}
	visited = cloneVisited(visited)
	visited[called.Path] = true
	newChain := chain + fmt.Sprintf("%s:%s -> ", caller.Path, job.ID)
	name := caller.ResolvedName() // github.workflow inside a called reusable workflow still
	// resolves to the CALLING workflow's name, per GitHub's docs on reusable workflows.

	var units []unit
	for _, cj := range called.Jobs {
		if cj.Concurrency != nil {
			units = append(units, unit{
				wfPath:     caller.Path,
				jobID:      job.ID + "/" + cj.ID,
				origin:     newChain + called.Path + ":" + cj.ID,
				events:     caller.Events,
				rawGroup:   cj.Concurrency.GroupRaw,
				parts:      substituteGithubWorkflow(cj.Concurrency.GroupParts, name),
				usesGHWF:   usesGithubWorkflow(cj.Concurrency.GroupParts),
				hasSymbol:  hasAnySymbol(cj.Concurrency.GroupParts),
				suppressed: cj.Concurrency.Suppressed,
				cip:        cj.Concurrency.CancelInProgress,
				cipExpr:    cj.Concurrency.CancelInProgressExpr,
				line:       cj.Concurrency.Line,
			})
		}
		units = append(units, collectCalledUnits(caller, cj, byPath, visited, newChain)...)
	}
	return units
}

func cloneVisited(v map[string]bool) map[string]bool {
	out := make(map[string]bool, len(v)+1)
	for k := range v {
		out[k] = true
	}
	return out
}

func checkDuplicateNames(workflows []*workflow.Workflow, units []unit) []Finding {
	byName := map[string][]*workflow.Workflow{}
	for _, w := range workflows {
		if strings.TrimSpace(w.Name) == "" {
			continue // path fallback is unique by construction
		}
		byName[w.Name] = append(byName[w.Name], w)
	}
	var findings []Finding
	for name, group := range byName {
		if len(group) < 2 {
			continue
		}
		var offenders []*workflow.Workflow
		for _, w := range group {
			if workflowUsesGithubWorkflow(w, units) {
				offenders = append(offenders, w)
			}
		}
		if len(offenders) == 0 {
			continue
		}
		var locs []Location
		for _, w := range group {
			locs = append(locs, Location{Path: w.Path, Line: w.NameLine})
		}
		sort.Slice(locs, func(i, j int) bool { return locs[i].Path < locs[j].Path })
		var paths []string
		for _, w := range group {
			paths = append(paths, w.Path)
		}
		sort.Strings(paths)
		findings = append(findings, Finding{
			Kind:     KindDuplicateWorkflowName,
			Severity: SeverityError,
			Message: fmt.Sprintf(
				"workflows %s all declare name: %q, so github.workflow resolves to the same string in each; at least one of them uses github.workflow in a concurrency group, which GitHub's docs say must be unique across workflows",
				strings.Join(paths, ", "), name,
			),
			Locations: locs,
		})
	}
	return findings
}

func workflowUsesGithubWorkflow(w *workflow.Workflow, units []unit) bool {
	for _, u := range units {
		if u.wfPath != w.Path {
			continue
		}
		if u.usesGHWF {
			return true
		}
	}
	return false
}

// checkSameRunNesting flags a job-level concurrency group identical to its
// own workflow's workflow-level group. GitHub's docs do not document what
// happens in this situation; concurclash flags it as a risk (the job would
// be queued behind/inside a concurrency slot its own run already holds),
// not as a confirmed deadlock — see the README.
func checkSameRunNesting(workflows []*workflow.Workflow) []Finding {
	var findings []Finding
	for _, w := range workflows {
		if w.Concurrency == nil {
			continue
		}
		wfName := w.ResolvedName()
		wfParts := substituteGithubWorkflow(w.Concurrency.GroupParts, wfName)
		for _, j := range w.Jobs {
			if j.Concurrency == nil {
				continue
			}
			jobParts := substituteGithubWorkflow(j.Concurrency.GroupParts, wfName)
			if sameRunEqual(wfParts, jobParts) {
				findings = append(findings, Finding{
					Kind:     KindSameGroupNesting,
					Severity: SeverityWarning,
					Message: fmt.Sprintf(
						"job %q's concurrency group is the same as its own workflow-level group (%q); GitHub does not document what happens here, so this is flagged as an undocumented risk, not a confirmed deadlock",
						j.ID, expr.Render(j.Concurrency.GroupParts),
					),
					Locations: []Location{
						{Path: w.Path, Line: w.Concurrency.Line, Group: w.Concurrency.GroupRaw},
						{Path: w.Path, JobID: j.ID, Line: j.Concurrency.Line, Group: j.Concurrency.GroupRaw},
					},
				})
			}
		}
	}
	return findings
}

func checkCrossRunCollisions(units []unit) []Finding {
	var findings []Finding
	for i := 0; i < len(units); i++ {
		for j := i + 1; j < len(units); j++ {
			a, b := units[i], units[j]
			if a.wfPath == b.wfPath && a.jobID == b.jobID {
				continue
			}
			overlap := mayOverlap(a.events, b.events)
			v := compareCrossRun(a.parts, b.parts, overlap)
			if v == vNone {
				continue
			}

			// A definite collision where NEITHER side's authored group
			// contains any "${{ ... }}" expression at all is a plain string
			// shared across workflows — often deliberate (a shared lock
			// name to serialize deploys), not a duplicate-name accident.
			// Downgrade it, and let it be acknowledged/suppressed.
			literalShared := v == vDefinite && !a.hasSymbol && !b.hasSymbol
			if literalShared && (a.suppressed || b.suppressed) {
				// Acknowledged via "# concurclash: shared" on either side:
				// no finding at all, including any cancel-in-progress note
				// for this same pair.
				continue
			}

			kind := KindPossibleCollision
			sev := SeverityWarning
			verb := "can"
			if v == vDefinite {
				verb = "will"
				kind = KindDefiniteCollision
				sev = SeverityError
			}

			locA := Location{Path: a.wfPath, JobID: a.jobID, Line: a.line, Group: a.rawGroup}
			locB := Location{Path: b.wfPath, JobID: b.jobID, Line: b.line, Group: b.rawGroup}

			var msg string
			if literalShared {
				kind = KindLiteralSharedGroup
				sev = SeverityWarning
				msg = fmt.Sprintf(
					"%s and %s share the literal concurrency group %q. This may be intentional "+
						"(e.g. serializing deploys to one environment), but GitHub's docs warn: "+
						`"By default, any existing pending job or workflow in the same concurrency `+
						`group will be canceled and the new queued job or workflow will take its `+
						`place." — a queued run of one can silently replace a queued run of the `+
						`other even with cancel-in-progress: false. If this sharing is intentional, `+
						"add a trailing \"# concurclash: shared\" comment on the group: line to "+
						"suppress this finding",
					locationLabel(locA), locationLabel(locB), a.rawGroup,
				)
			} else {
				msg = fmt.Sprintf(
					"%s and %s %s resolve to the same concurrency group (%q vs %q)",
					locationLabel(locA), locationLabel(locB), verb,
					expr.Render(a.parts), expr.Render(b.parts),
				)
			}

			findings = append(findings, Finding{
				Kind:      kind,
				Severity:  sev,
				Message:   msg,
				Locations: []Location{locA, locB},
			})
			if a.cip != nil && b.cip != nil && *a.cip != *b.cip {
				findings = append(findings, Finding{
					Kind:     KindCancelMismatch,
					Severity: SeverityInfo,
					Message: fmt.Sprintf(
						"%s has cancel-in-progress: %v but %s has cancel-in-progress: %v, for groups that %s collide",
						locationLabel(locA), *a.cip, locationLabel(locB), *b.cip, verb,
					),
					Locations: []Location{locA, locB},
				})
			}
		}
	}
	return findings
}

func locationLabel(l Location) string {
	if l.JobID == "" {
		return l.Path
	}
	return l.Path + ":" + l.JobID
}
