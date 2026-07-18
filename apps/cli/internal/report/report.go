// Package report renders an eval GateResult into the artifacts a CI pipeline
// consumes: JUnit XML (one testcase per gate check), a Markdown summary (for a
// PR comment), and the process exit code. Kept free of any network or CLI
// concern so it is unit-testable in isolation.
package report

import (
	"encoding/xml"
	"fmt"
	"strings"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ExitCode maps a gate result to a process exit code: 0 when the gate passed,
// 1 otherwise (a failed OR inconclusive gate blocks a release in CI).
func ExitCode(res *gantryv1.GateResult) int {
	if res != nil && res.Passed {
		return 0
	}
	return 1
}

// Status is a short human label for the gate outcome.
func Status(res *gantryv1.GateResult) string {
	switch {
	case res == nil:
		return "ERROR"
	case res.Inconclusive:
		return "INCONCLUSIVE"
	case res.Passed:
		return "PASS"
	default:
		return "FAIL"
	}
}

type junitSuite struct {
	XMLName  xml.Name    `xml:"testsuite"`
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// JUnit renders the gate as a JUnit test suite: one testcase per gate check,
// with a <failure> for any check that did not pass. An inconclusive gate with no
// per-check failures still yields one failing case so CI shows red.
func JUnit(res *gantryv1.GateResult, suiteName string) (string, error) {
	suite := junitSuite{Name: "gantry-gate:" + suiteName}
	for _, c := range res.GetChecks() {
		tc := junitCase{Name: c.Metric + " " + c.Op, Classname: suiteName}
		if !c.Passed {
			suite.Failures++
			tc.Failure = &junitFailure{Message: c.Detail, Body: c.Detail}
		}
		suite.Cases = append(suite.Cases, tc)
	}
	if res.GetInconclusive() && suite.Failures == 0 {
		suite.Failures++
		suite.Cases = append(suite.Cases, junitCase{
			Name: "gate", Classname: suiteName,
			Failure: &junitFailure{Message: "inconclusive", Body: "too few trials scored to decide"},
		})
	}
	suite.Tests = len(suite.Cases)
	out, err := xml.MarshalIndent(suite, "", "  ")
	if err != nil {
		return "", fmt.Errorf("report: marshal junit: %w", err)
	}
	return xml.Header + string(out) + "\n", nil
}

// Markdown renders a compact summary suitable for a PR comment.
func Markdown(res *gantryv1.GateResult) string {
	var b strings.Builder
	icon := map[string]string{"PASS": "✅", "FAIL": "❌", "INCONCLUSIVE": "⚠️", "ERROR": "🚫"}[Status(res)]
	fmt.Fprintf(&b, "### Gate: %s %s\n\n", Status(res), icon)
	if res == nil {
		return b.String()
	}
	if cand := res.GetCandidate(); cand != nil {
		fmt.Fprintf(&b, "candidate `%s`", subjectRef(cand))
		if base := res.GetBaseline(); base != nil {
			fmt.Fprintf(&b, " vs baseline `%s`", subjectRef(base))
		} else {
			b.WriteString(" (no baseline — bootstrap)")
		}
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "trials: **%d** pass / **%d** fail / **%d** void\n\n", res.Pass, res.Fail, res.Void)

	b.WriteString("| metric | candidate | baseline | op | margin | result | detail |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, c := range res.GetChecks() {
		mark := "❌"
		if c.Passed {
			mark = "✅"
		}
		fmt.Fprintf(&b, "| %s | %.4g | %.4g | %s | %.4g | %s | %s |\n",
			c.Metric, c.CandidateValue, c.BaselineValue, c.Op, c.Margin, mark, c.Detail)
	}
	return b.String()
}

func subjectRef(s *gantryv1.Subject) string {
	if s.Version != "" {
		return s.Version
	}
	if s.Digest != "" {
		return s.Digest
	}
	return s.Uri
}
