// Package report formats detect.Finding slices for a human (text), a
// machine (json), or GitHub Actions' own log-annotation syntax (github).
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/quietpelican42/concurclash/internal/detect"
)

// Format selects the output writer used by Write.
type Format string

const (
	FormatText   Format = "text"
	FormatJSON   Format = "json"
	FormatGitHub Format = "github"
)

// Write renders findings to w in the given format. An unrecognized format
// is an error the caller should treat as a usage error (exit 2), not a
// finding.
func Write(w io.Writer, format Format, findings []detect.Finding) error {
	switch format {
	case FormatText, "":
		return writeText(w, findings)
	case FormatJSON:
		return writeJSON(w, findings)
	case FormatGitHub:
		return writeGithub(w, findings)
	default:
		return fmt.Errorf("unknown format %q (want text, json, or github)", format)
	}
}

func writeText(w io.Writer, findings []detect.Finding) error {
	if len(findings) == 0 {
		_, err := fmt.Fprintln(w, "concurclash: no concurrency group collisions found")
		return err
	}
	counts := map[detect.Severity]int{}
	for _, f := range findings {
		counts[f.Severity]++
		loc := locString(f.Locations)
		if _, err := fmt.Fprintf(w, "%s: [%s] %s\n    %s\n", strings.ToUpper(string(f.Severity)), f.Kind, f.Message, loc); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "\n%d error(s), %d warning(s), %d info\n",
		counts[detect.SeverityError], counts[detect.SeverityWarning], counts[detect.SeverityInfo])
	return err
}

func locString(locs []detect.Location) string {
	var parts []string
	for _, l := range locs {
		s := l.Path
		if l.JobID != "" {
			s += ":" + l.JobID
		}
		if l.Line > 0 {
			s += fmt.Sprintf(":%d", l.Line)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "  <->  ")
}

func writeJSON(w io.Writer, findings []detect.Finding) error {
	if findings == nil {
		findings = []detect.Finding{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(findings)
}

// writeGithub emits GitHub Actions workflow-command annotations
// ("::error file=...,line=...::message"), documented at
// https://docs.github.com/en/actions/using-workflows/workflow-commands-for-github-actions
func writeGithub(w io.Writer, findings []detect.Finding) error {
	for _, f := range findings {
		cmd := "notice"
		switch f.Severity {
		case detect.SeverityError:
			cmd = "error"
		case detect.SeverityWarning:
			cmd = "warning"
		}
		file := ""
		line := 0
		if len(f.Locations) > 0 {
			file = f.Locations[0].Path
			line = f.Locations[0].Line
		}
		msg := escapeGithub(fmt.Sprintf("[%s] %s (%s)", f.Kind, f.Message, locString(f.Locations)))
		if file != "" {
			if line > 0 {
				if _, err := fmt.Fprintf(w, "::%s file=%s,line=%d::%s\n", cmd, file, line, msg); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(w, "::%s file=%s::%s\n", cmd, file, msg); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "::%s::%s\n", cmd, msg); err != nil {
			return err
		}
	}
	return nil
}

func escapeGithub(s string) string {
	r := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	return r.Replace(s)
}
