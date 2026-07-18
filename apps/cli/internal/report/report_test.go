package report

import (
	"strings"
	"testing"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

func passing() *gantryv1.GateResult {
	return &gantryv1.GateResult{
		Passed: true, Pass: 46, Fail: 4,
		Candidate: &gantryv1.Subject{Version: "rc1"},
		Baseline:  &gantryv1.Subject{Version: "v0"},
		Checks: []*gantryv1.GateCheck{
			{Metric: "success_rate", Op: "non_inferior", CandidateValue: 0.92, BaselineValue: 0.90, Margin: 0.03, Passed: true, Detail: "Wilson 95% lower bound 0.88 >= 0.87"},
		},
	}
}

func TestExitCodeAndStatus(t *testing.T) {
	if ExitCode(passing()) != 0 {
		t.Fatal("passing gate should exit 0")
	}
	fail := &gantryv1.GateResult{Passed: false}
	if ExitCode(fail) != 1 || Status(fail) != "FAIL" {
		t.Fatal("failed gate should exit 1 / FAIL")
	}
	inc := &gantryv1.GateResult{Passed: false, Inconclusive: true}
	if ExitCode(inc) != 1 || Status(inc) != "INCONCLUSIVE" {
		t.Fatal("inconclusive gate should exit 1 / INCONCLUSIVE")
	}
	if ExitCode(nil) != 1 || Status(nil) != "ERROR" {
		t.Fatal("nil result should exit 1 / ERROR")
	}
}

func TestJUnit(t *testing.T) {
	// One passing + one failing check.
	res := passing()
	res.Passed = false
	res.Checks = append(res.Checks, &gantryv1.GateCheck{Metric: "task_time_s", Op: "<=", Passed: false, Detail: "41 > 30"})
	out, err := JUnit(res, "arm-pickplace")
	if err != nil {
		t.Fatalf("JUnit: %v", err)
	}
	if !strings.Contains(out, `tests="2"`) || !strings.Contains(out, `failures="1"`) {
		t.Fatalf("junit counts wrong:\n%s", out)
	}
	if !strings.Contains(out, "<failure") || !strings.Contains(out, "41 &gt; 30") {
		t.Fatalf("junit missing escaped failure:\n%s", out)
	}
}

func TestJUnitInconclusiveYieldsFailure(t *testing.T) {
	out, err := JUnit(&gantryv1.GateResult{Inconclusive: true}, "s")
	if err != nil {
		t.Fatalf("JUnit: %v", err)
	}
	if !strings.Contains(out, `failures="1"`) || !strings.Contains(out, "inconclusive") {
		t.Fatalf("inconclusive gate should render a failing case:\n%s", out)
	}
}

func TestMarkdown(t *testing.T) {
	md := Markdown(passing())
	if !strings.Contains(md, "Gate: PASS") {
		t.Fatalf("markdown missing status:\n%s", md)
	}
	if !strings.Contains(md, "success_rate") || !strings.Contains(md, "46** pass") {
		t.Fatalf("markdown missing checks/tallies:\n%s", md)
	}
}
