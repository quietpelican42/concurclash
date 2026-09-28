package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/__GH_OWNER__/concurclash/internal/detect"
)

func sample() []detect.Finding {
	return []detect.Finding{
		{
			Kind:     detect.KindDefiniteCollision,
			Severity: detect.SeverityError,
			Message:  "a.yml and b.yml will resolve to the same concurrency group",
			Locations: []detect.Location{
				{Path: ".github/workflows/a.yml", Line: 4, Group: "g"},
				{Path: ".github/workflows/b.yml", Line: 4, Group: "g"},
			},
		},
	}
}

func TestWriteTextNoFindings(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, FormatText, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no concurrency group collisions found") {
		t.Errorf("got %q", buf.String())
	}
}

func TestWriteTextWithFindings(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, FormatText, sample()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "ERROR") || !strings.Contains(out, "a.yml") || !strings.Contains(out, "b.yml") {
		t.Errorf("text output missing expected content: %q", out)
	}
	if !strings.Contains(out, "1 error(s), 0 warning(s), 0 info") {
		t.Errorf("text output missing summary line: %q", out)
	}
}

func TestWriteJSONRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	findings := sample()
	if err := Write(&buf, FormatJSON, findings); err != nil {
		t.Fatal(err)
	}
	var got []detect.Finding
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0].Kind != detect.KindDefiniteCollision {
		t.Errorf("got %+v", got)
	}
}

func TestWriteJSONEmptyIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, FormatJSON, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Errorf("expected an empty JSON array, got %q", buf.String())
	}
}

func TestWriteGithubAnnotations(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, FormatGitHub, sample()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "::error file=.github/workflows/a.yml,line=4::") {
		t.Errorf("got %q", out)
	}
}

func TestWriteUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "bogus", nil); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}

func TestGithubEscaping(t *testing.T) {
	findings := []detect.Finding{{
		Kind:      detect.KindPossibleCollision,
		Severity:  detect.SeverityWarning,
		Message:   "line one\nline two % percent",
		Locations: []detect.Location{{Path: "x.yml"}},
	}}
	var buf bytes.Buffer
	if err := Write(&buf, FormatGitHub, findings); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "\n") && !strings.HasSuffix(out, "\n") {
		t.Errorf("unescaped newline in the middle of an annotation: %q", out)
	}
	if !strings.Contains(out, "%0A") || !strings.Contains(out, "%25") {
		t.Errorf("expected escaped newline/percent, got %q", out)
	}
}
