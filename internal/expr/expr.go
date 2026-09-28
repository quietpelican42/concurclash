// Package expr parses GitHub Actions "${{ ... }}" expression strings that
// appear inside a concurrency group value, and classifies the pieces so two
// group expressions from different workflows can be compared symbolically
// without a full GitHub Actions expression evaluator.
package expr

import (
	"regexp"
	"strings"
)

// Kind distinguishes a literal text segment from an interpolated expression.
type Kind int

const (
	// Literal is plain text copied verbatim into the group string.
	Literal Kind = iota
	// Symbol is the contents of a "${{ ... }}" block. Its runtime value is
	// not known statically; Class records what can be assumed about it.
	Symbol
)

// Class records what we can assume about a Symbol's runtime value when
// comparing two group expressions.
type Class int

const (
	// NotSymbol is the zero value for Literal parts.
	NotSymbol Class = iota
	// RepoConstant is always identical across every workflow run in the
	// same repository (github.repository, github.repository_owner).
	RepoConstant
	// RefShared is identical between two runs only when both runs share
	// the same underlying ref/event (github.ref, github.head_ref, ...).
	// Whether that holds depends on the two workflows' triggers.
	RefShared
	// RunUnique is guaranteed different on every run (github.run_id,
	// github.run_number, github.run_attempt). A group containing one can
	// never collide with another run's group through that part.
	RunUnique
	// Variable can differ or coincide depending on caller-supplied data
	// that this tool cannot see (inputs.*, matrix.*, env.*, vars.*,
	// secrets.*, github.event.*).
	Variable
	// Opaque is any other expression (function calls, unrecognized
	// context paths). Treated conservatively like Variable.
	Opaque
)

// Part is one literal or symbolic segment of a concurrency group string.
type Part struct {
	Kind Kind
	// Text is the literal text for a Literal part, or the trimmed,
	// lower-cased expression text for a Symbol part (its canonical form).
	Text string
	// Raw is the original, un-lower-cased expression text for a Symbol
	// part. Empty for Literal parts.
	Raw   string
	Class Class
}

var interpRe = regexp.MustCompile(`\$\{\{(.*?)\}\}`)

// Parse splits a raw concurrency group string into literal and symbol
// parts, in order. Symbol parts are classified by Classify.
func Parse(raw string) []Part {
	var parts []Part
	idx := interpRe.FindAllStringSubmatchIndex(raw, -1)
	last := 0
	for _, m := range idx {
		start, end := m[0], m[1]
		exprStart, exprEnd := m[2], m[3]
		if start > last {
			parts = append(parts, Part{Kind: Literal, Text: raw[last:start]})
		}
		rawExpr := raw[exprStart:exprEnd]
		canon := strings.ToLower(strings.TrimSpace(rawExpr))
		parts = append(parts, Part{
			Kind:  Symbol,
			Text:  canon,
			Raw:   strings.TrimSpace(rawExpr),
			Class: Classify(canon),
		})
		last = end
	}
	if last < len(raw) {
		parts = append(parts, Part{Kind: Literal, Text: raw[last:]})
	}
	return parts
}

var repoConstants = map[string]bool{
	"github.repository":       true,
	"github.repository_owner": true,
	"github.repository_id":    true,
}

var refShared = map[string]bool{
	"github.ref":                       true,
	"github.ref_name":                  true,
	"github.head_ref":                  true,
	"github.base_ref":                  true,
	"github.sha":                       true,
	"github.event_name":                true,
	"github.job":                       true,
	"github.actor":                     true,
	"github.event.pull_request.number": true,
	"github.event.number":              true,
	"github.event.issue.number":        true,
}

var runUnique = map[string]bool{
	"github.run_id":      true,
	"github.run_number":  true,
	"github.run_attempt": true,
}

var variablePrefixes = []string{
	"inputs.", "matrix.", "env.", "vars.", "secrets.", "github.event.",
}

// IsGithubWorkflow reports whether a canonical symbol expression is exactly
// the github.workflow context property, the one symbol this tool resolves
// to a concrete literal (the workflow's name, or file path — see
// workflow.ResolvedName).
func IsGithubWorkflow(canon string) bool {
	return canon == "github.workflow"
}

// Classify assigns a Class to a canonical (trimmed, lower-cased) expression
// string. Unrecognized expressions are classified Opaque and compared
// conservatively (never assumed equal, never assumed unequal).
func Classify(canon string) Class {
	switch {
	case repoConstants[canon]:
		return RepoConstant
	case refShared[canon]:
		return RefShared
	case runUnique[canon]:
		return RunUnique
	}
	for _, p := range variablePrefixes {
		if strings.HasPrefix(canon, p) {
			return Variable
		}
	}
	return Opaque
}

// Render reassembles parts back into the group string they were parsed
// from (using each Symbol's Raw form), for display in reports.
func Render(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == Literal {
			b.WriteString(p.Text)
		} else {
			b.WriteString("${{ ")
			b.WriteString(p.Raw)
			b.WriteString(" }}")
		}
	}
	return b.String()
}
