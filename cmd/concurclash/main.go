// Command concurclash finds GitHub Actions concurrency: group collisions
// across a repository's workflows — cases where two workflows or jobs can
// compute the same concurrency group and cancel or queue behind each
// other, which GitHub's own linter (actionlint) and security scanner
// (zizmor) do not check for. See the README for what it can and cannot
// prove.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/__GH_OWNER__/concurclash/internal/detect"
	"github.com/__GH_OWNER__/concurclash/internal/report"
	"github.com/__GH_OWNER__/concurclash/internal/workflow"
)

const usage = `concurclash [flags] [path]

Scans <path>/.github/workflows (default: current directory) for
concurrency: group collisions between workflows and jobs.

Flags:
  -format string   Output format: text, json, or github (default "text")

Exit codes:
  0  no findings
  1  one or more findings
  2  could not analyse (unreadable directory, invalid workflow YAML)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("concurclash", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "text", "output format: text, json, or github")
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root := "."
	if fs.NArg() > 0 {
		root = fs.Arg(0)
	}

	workflows, errs := workflow.LoadDir(root)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(stderr, "concurclash:", e)
		}
		if len(workflows) == 0 {
			return 2
		}
		// Partial failure: some files parsed. Report on what we have, but
		// still signal analysis was incomplete.
	}
	if len(workflows) == 0 && len(errs) == 0 {
		fmt.Fprintf(stderr, "concurclash: no workflow files found under %s/.github/workflows\n", root)
		return 2
	}

	findings := detect.Analyze(workflows)

	if err := report.Write(stdout, report.Format(*format), findings); err != nil {
		fmt.Fprintln(stderr, "concurclash:", err)
		return 2
	}

	if len(errs) > 0 {
		return 2
	}
	if len(findings) > 0 {
		return 1
	}
	return 0
}
