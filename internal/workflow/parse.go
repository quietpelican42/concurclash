// Package workflow parses GitHub Actions workflow YAML files into the
// subset of structure concurclash needs: triggers, and workflow- and
// job-level concurrency blocks.
package workflow

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/quietpelican42/concurclash/internal/expr"
)

// Concurrency is a parsed workflow- or job-level "concurrency:" block.
type Concurrency struct {
	GroupRaw string
	// GroupParts is GroupRaw split into literal/symbol parts. github.workflow
	// symbols are NOT yet substituted here; that happens per-owning-workflow
	// in the detect package, since the substitution value depends on which
	// workflow the block belongs to.
	GroupParts []expr.Part
	// CancelInProgress is nil when the value is a runtime expression this
	// tool cannot resolve statically; otherwise it holds the literal true/false
	// (defaulting to false when the field is omitted, per GitHub's docs).
	CancelInProgress *bool
	// CancelInProgressExpr holds the raw expression text when
	// CancelInProgress is nil because the value was "${{ ... }}".
	CancelInProgressExpr string
	Line                 int
	// Suppressed is true when the "group" line carries a trailing
	// "# concurclash: shared" comment, acknowledging that this group is
	// deliberately shared with another workflow (e.g. to serialize deploys
	// to one environment). See the README's "Suppressing an intentional
	// collision" section.
	Suppressed bool
}

// Job is one job entry from a workflow's "jobs:" map.
type Job struct {
	ID          string
	Name        string
	Uses        string // raw "uses:" value, e.g. "./.github/workflows/reusable.yml"
	Concurrency *Concurrency
	Line        int
}

// Workflow is one parsed workflow file.
type Workflow struct {
	// Path is the file path relative to the repository root, using forward
	// slashes, e.g. ".github/workflows/ci.yml".
	Path string
	// Name is the raw "name:" field, or "" if the workflow has none.
	Name     string
	NameLine int
	// Events is the set of trigger event names found under "on:". Filters
	// (branches, paths, schedule cron, etc.) are not modeled; see the
	// README's "What it approximates" section.
	Events map[string]bool
	// Concurrency is the workflow-level block, nil if absent.
	Concurrency *Concurrency
	Jobs        []*Job
}

// ResolvedName is the value github.workflow takes for this workflow: the
// declared name, or (per GitHub's documented fallback) the workflow file's
// path when no name is set.
func (w *Workflow) ResolvedName() string {
	if strings.TrimSpace(w.Name) != "" {
		return w.Name
	}
	return w.Path
}

// LoadDir finds every *.yml / *.yaml file directly under
// "<root>/.github/workflows" and parses it. Parse errors for individual
// files are collected and returned rather than aborting the whole run, so
// one malformed workflow doesn't hide findings in the rest of the repo.
func LoadDir(root string) ([]*Workflow, []error) {
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{fmt.Errorf("read %s: %w", dir, err)}
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext == ".yml" || ext == ".yaml" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var workflows []*Workflow
	var errs []error
	for _, name := range names {
		full := filepath.Join(dir, name)
		data, err := os.ReadFile(full)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", full, err))
			continue
		}
		rel := filepath.ToSlash(filepath.Join(".github", "workflows", name))
		wf, err := Parse(rel, data)
		if err != nil {
			errs = append(errs, fmt.Errorf("parse %s: %w", rel, err))
			continue
		}
		workflows = append(workflows, wf)
	}
	return workflows, errs
}

// Parse parses the YAML content of a single workflow file. path is the
// repo-relative path recorded on the resulting Workflow (and used as the
// github.workflow fallback value).
func Parse(path string, data []byte) (*Workflow, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid yaml: %w", err)
	}
	if len(doc.Content) == 0 {
		return &Workflow{Path: path, Events: map[string]bool{}}, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("top-level document is not a mapping")
	}

	wf := &Workflow{Path: path, Events: map[string]bool{}}

	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		val := root.Content[i+1]
		// YAML 1.1 parses the bare key "on" as the boolean true; yaml.v3
		// keeps key.Value as the literal source text ("on"), so a plain
		// string compare is safe here.
		switch strings.ToLower(key.Value) {
		case "name":
			wf.Name = val.Value
			wf.NameLine = key.Line
		case "on":
			wf.Events = parseOn(val)
		case "concurrency":
			c, err := parseConcurrency(val)
			if err != nil {
				return nil, fmt.Errorf("workflow concurrency: %w", err)
			}
			wf.Concurrency = c
		case "jobs":
			jobs, err := parseJobs(val)
			if err != nil {
				return nil, err
			}
			wf.Jobs = jobs
		}
	}
	return wf, nil
}

func parseOn(node *yaml.Node) map[string]bool {
	events := map[string]bool{}
	if node == nil {
		return events
	}
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value != "" {
			events[node.Value] = true
		}
	case yaml.SequenceNode:
		for _, c := range node.Content {
			if c.Kind == yaml.ScalarNode {
				events[c.Value] = true
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			k := node.Content[i]
			if k.Kind == yaml.ScalarNode {
				events[k.Value] = true
			}
		}
	}
	return events
}

func parseConcurrency(node *yaml.Node) (*Concurrency, error) {
	if node == nil || node.Kind == 0 {
		return nil, nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return &Concurrency{
			GroupRaw:         node.Value,
			GroupParts:       expr.Parse(node.Value),
			CancelInProgress: boolPtr(false),
			Line:             node.Line,
			Suppressed:       isSharedSuppression(node.LineComment),
		}, nil
	case yaml.MappingNode:
		c := &Concurrency{CancelInProgress: boolPtr(false), Line: node.Line}
		for i := 0; i+1 < len(node.Content); i += 2 {
			k := node.Content[i]
			v := node.Content[i+1]
			switch strings.ToLower(k.Value) {
			case "group":
				c.GroupRaw = v.Value
				c.GroupParts = expr.Parse(v.Value)
				c.Suppressed = isSharedSuppression(v.LineComment)
				if c.Line == 0 {
					c.Line = k.Line
				}
			case "cancel-in-progress":
				if strings.Contains(v.Value, "${{") {
					c.CancelInProgress = nil
					c.CancelInProgressExpr = v.Value
				} else {
					b := strings.EqualFold(strings.TrimSpace(v.Value), "true")
					c.CancelInProgress = &b
				}
			}
		}
		if c.GroupRaw == "" && c.GroupParts == nil {
			return nil, fmt.Errorf("concurrency mapping has no 'group' key at line %d", node.Line)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unsupported concurrency node kind at line %d", node.Line)
	}
}

func parseJobs(node *yaml.Node) ([]*Job, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, nil
	}
	var jobs []*Job
	for i := 0; i+1 < len(node.Content); i += 2 {
		idNode := node.Content[i]
		jobNode := node.Content[i+1]
		job := &Job{ID: idNode.Value, Line: jobNode.Line}
		if jobNode.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(jobNode.Content); j += 2 {
				k := jobNode.Content[j]
				v := jobNode.Content[j+1]
				switch strings.ToLower(k.Value) {
				case "name":
					job.Name = v.Value
				case "uses":
					job.Uses = v.Value
				case "concurrency":
					c, err := parseConcurrency(v)
					if err != nil {
						return nil, fmt.Errorf("job %q concurrency: %w", job.ID, err)
					}
					job.Concurrency = c
				}
			}
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func boolPtr(b bool) *bool { return &b }

// isSharedSuppression reports whether a YAML trailing comment marks its
// group as an acknowledged, intentionally shared concurrency group, e.g.:
//
//	concurrency:
//	  group: production-deploy  # concurclash: shared
//
// Matching is deliberately loose (case-insensitive, tolerant of missing
// space after the colon) since this is a human-written annotation.
func isSharedSuppression(comment string) bool {
	c := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(comment), "#")))
	c = strings.TrimSpace(c)
	return c == "concurclash: shared" || c == "concurclash:shared"
}

// FS-based variant kept for future use / testing against an fs.FS rooted
// at the repository (not currently used by the CLI, which reads the real
// filesystem directly via LoadDir, but useful in tests against embedded
// fixtures without touching disk paths).
func LoadFS(fsys fs.FS, workflowsDir string) ([]*Workflow, []error) {
	entries, err := fs.ReadDir(fsys, workflowsDir)
	if err != nil {
		return nil, []error{fmt.Errorf("read %s: %w", workflowsDir, err)}
	}
	var workflows []*Workflow
	var errs []error
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext == ".yml" || ext == ".yaml" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := fs.ReadFile(fsys, filepath.Join(workflowsDir, name))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rel := filepath.ToSlash(filepath.Join(".github", "workflows", name))
		wf, err := Parse(rel, data)
		if err != nil {
			errs = append(errs, fmt.Errorf("parse %s: %w", rel, err))
			continue
		}
		workflows = append(workflows, wf)
	}
	return workflows, errs
}
