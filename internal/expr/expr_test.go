package expr

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		parts []Part
	}{
		{
			name: "pure literal",
			raw:  "prod-deploy",
			parts: []Part{
				{Kind: Literal, Text: "prod-deploy"},
			},
		},
		{
			name: "literal plus one symbol",
			raw:  "${{ github.workflow }}-${{ github.ref }}",
			parts: []Part{
				{Kind: Symbol, Text: "github.workflow", Raw: "github.workflow", Class: Opaque},
				{Kind: Literal, Text: "-"},
				{Kind: Symbol, Text: "github.ref", Raw: "github.ref", Class: RefShared},
			},
		},
		{
			name: "whitespace inside interpolation is trimmed for canonical form",
			raw:  "${{   GITHUB.REF   }}",
			parts: []Part{
				{Kind: Symbol, Text: "github.ref", Raw: "GITHUB.REF", Class: RefShared},
			},
		},
		{
			name: "leading and trailing literal around symbol",
			raw:  "pre-${{ inputs.env }}-post",
			parts: []Part{
				{Kind: Literal, Text: "pre-"},
				{Kind: Symbol, Text: "inputs.env", Raw: "inputs.env", Class: Variable},
				{Kind: Literal, Text: "-post"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Parse(c.raw)
			if len(got) != len(c.parts) {
				t.Fatalf("Parse(%q) = %#v, want %#v", c.raw, got, c.parts)
			}
			for i := range got {
				if got[i] != c.parts[i] {
					t.Errorf("part %d: got %#v, want %#v", i, got[i], c.parts[i])
				}
			}
		})
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]Class{
		"github.repository":       RepoConstant,
		"github.repository_owner": RepoConstant,
		"github.ref":              RefShared,
		"github.head_ref":         RefShared,
		"github.run_id":           RunUnique,
		"github.run_number":       RunUnique,
		"inputs.environment":      Variable,
		"matrix.os":               Variable,
		"env.FOO":                 Variable,
		"github.event.action":     Variable,
		"github.workflow":         Opaque, // handled specially via IsGithubWorkflow, not Classify
		"format('x-{0}', 1)":      Opaque,
	}
	for expr, want := range cases {
		if got := Classify(expr); got != want {
			t.Errorf("Classify(%q) = %v, want %v", expr, got, want)
		}
	}
}

func TestIsGithubWorkflow(t *testing.T) {
	if !IsGithubWorkflow("github.workflow") {
		t.Error("expected github.workflow to be recognized")
	}
	if IsGithubWorkflow("github.workflowx") {
		t.Error("did not expect a prefix match")
	}
	if IsGithubWorkflow("github.ref") {
		t.Error("did not expect github.ref to match")
	}
}

func TestRenderRoundTrip(t *testing.T) {
	raw := "${{ github.workflow }}-${{ github.ref }}"
	parts := Parse(raw)
	got := Render(parts)
	want := "${{ github.workflow }}-${{ github.ref }}"
	if got != want {
		t.Errorf("Render(Parse(%q)) = %q, want %q", raw, got, want)
	}
}
