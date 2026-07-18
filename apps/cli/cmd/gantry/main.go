// Command gantry is the CI-facing client for Gantry evals: it evaluates a run's
// release gate against the baseline and emits CI artifacts (JUnit, Markdown,
// JSON), exiting 0 on pass and non-zero otherwise — so a bench gate drops into
// any pipeline as "just another step". It talks to a Bench or Cloud endpoint
// over the same ConnectRPC surface; --endpoint chooses which.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	connect "connectrpc.com/connect"
	"github.com/Sammyjroberts/gantry/apps/cli/internal/report"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() { os.Exit(run(os.Args[1:])) }

// run dispatches subcommands and returns a process exit code. Exit codes:
// 0 = gate passed, 1 = gate failed/inconclusive, 2 = usage/transport error.
func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "gate":
		return cmdGate(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "gantry: unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `gantry — CI client for Gantry evals

usage:
  gantry gate [flags]      evaluate a run's release gate and emit CI reports

gate flags:
  --endpoint URL           bench/cloud base URL (default http://localhost:4780)
  --token TOKEN            scoped access token (Authorization: Bearer)
  --run ID                run to gate (or use --suite/--candidate to start one)
  --suite ID              start (or re-attach to) a run for this suite
  --candidate DIGEST      candidate subject digest (with --suite)
  --baseline REF          baseline ref to compare against (default "latest")
  --target SELECTOR       station tag selector for the run
  --idempotency-key KEY   makes StartRun retry-safe (e.g. $GITHUB_RUN_ID)
  --report SPEC           comma list: junit=PATH,md=PATH,json=PATH
  --promote-on-pass       promote the candidate to baseline when the gate passes
`)
}

func cmdGate(args []string) int {
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	var (
		endpoint   = fs.String("endpoint", "http://localhost:4780", "bench/cloud base URL")
		token      = fs.String("token", "", "scoped access token")
		runID      = fs.String("run", "", "run id to gate")
		suiteID    = fs.String("suite", "", "suite id (start/re-attach a run)")
		candidate  = fs.String("candidate", "", "candidate subject digest")
		baseline   = fs.String("baseline", "latest", "baseline ref")
		target     = fs.String("target", "", "station tag selector")
		idemKey    = fs.String("idempotency-key", "", "StartRun idempotency key")
		reportSpec = fs.String("report", "", "junit=PATH,md=PATH,json=PATH")
		promote    = fs.Bool("promote-on-pass", false, "promote on pass")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runID == "" && (*suiteID == "" || *candidate == "") {
		fmt.Fprintln(os.Stderr, "gantry gate: need --run, or both --suite and --candidate")
		return 2
	}

	ctx := context.Background()
	client := gantryv1connect.NewEvalServiceClient(http.DefaultClient, *endpoint,
		connect.WithInterceptors(bearer(*token)))

	// Resolve the run: an explicit --run, or start/re-attach one for a suite.
	rid := *runID
	if rid == "" {
		start, err := client.StartRun(ctx, connect.NewRequest(&gantryv1.StartRunRequest{
			SuiteId: *suiteID, BaselineRef: *baseline, TargetSelector: *target,
			Candidate:      &gantryv1.Subject{Kind: "policy", Digest: *candidate},
			IdempotencyKey: *idemKey,
		}))
		if err != nil {
			fmt.Fprintf(os.Stderr, "gantry gate: start run: %v\n", err)
			return 2
		}
		rid = start.Msg.Run.Id
	}

	gate, err := client.EvaluateGate(ctx, connect.NewRequest(&gantryv1.EvaluateGateRequest{RunId: rid}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gantry gate: evaluate: %v\n", err)
		return 2
	}
	res := gate.Msg.Result

	// Resolve a display name for reports (best-effort).
	suiteName := *suiteID
	if run, err := client.GetRun(ctx, connect.NewRequest(&gantryv1.GetRunRequest{Id: rid})); err == nil {
		suiteName = run.Msg.Run.SuiteId
	}

	if code := emitReports(*reportSpec, res, suiteName); code != 0 {
		return code
	}
	fmt.Println(report.Markdown(res))

	if *promote && res.Passed {
		if _, err := client.PromoteBaseline(ctx, connect.NewRequest(&gantryv1.PromoteBaselineRequest{
			RunId: rid, IdempotencyKey: *idemKey,
		})); err != nil {
			fmt.Fprintf(os.Stderr, "gantry gate: promote: %v\n", err)
			return 2
		}
		fmt.Fprintln(os.Stderr, "gantry gate: promoted candidate to baseline")
	}
	return report.ExitCode(res)
}

// emitReports writes the requested artifacts. Returns a non-zero exit code only
// on a write/render error (2).
func emitReports(spec string, res *gantryv1.GateResult, suiteName string) int {
	for _, part := range splitNonEmpty(spec, ",") {
		kind, path, ok := strings.Cut(part, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "gantry gate: bad --report entry %q\n", part)
			return 2
		}
		var (
			body string
			err  error
		)
		switch kind {
		case "junit":
			body, err = report.JUnit(res, suiteName)
		case "md":
			body = report.Markdown(res)
		case "json":
			var b []byte
			b, err = protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(res)
			body = string(b)
		default:
			fmt.Fprintf(os.Stderr, "gantry gate: unknown report kind %q\n", kind)
			return 2
		}
		if err == nil {
			err = os.WriteFile(path, []byte(body), 0o644)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "gantry gate: write %s: %v\n", path, err)
			return 2
		}
	}
	return 0
}

func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, sep)
}

// bearer adds an Authorization header to every request when a token is set.
func bearer(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token != "" {
				req.Header().Set("Authorization", "Bearer "+token)
			}
			return next(ctx, req)
		}
	}
}
