package mcp

import (
	"context"
	"fmt"

	"github.com/Sammyjroberts/gantry/core/go/auth"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerExperimentTools wires the experiment write/read tools onto s. Called
// only when Deps.Experiments is non-nil. Unlike the telemetry tools, these
// mutate state (start/stop), so they are gated on the hosting app supplying an
// experiment engine.
func registerExperimentTools(s *mcpsdk.Server, d Deps) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "start_experiment",
		Description: "Start (open) a named experiment — a time range over the telemetry stream used to bracket a maneuver or test for later review/export. Begins now. Returns the experiment (id, name, start_ns, end_ns, running). Use stop_experiment with the returned id to close it.",
	}, d.startExperiment)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "stop_experiment",
		Description: "Stop (close) a running experiment by id, ending it now. Returns the updated experiment; running becomes false and end_ns is set.",
	}, d.stopExperiment)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_experiments",
		Description: "List experiments newest-first, optionally filtered to one device. Read-only. Returns each experiment's id, name, start_ns, end_ns, and running flag.",
	}, d.listExperiments)
}

// experimentResult is the JSON echo of an experiment returned by the tools.
type experimentResult struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Notes    string `json:"notes,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
	StartNs  uint64 `json:"start_ns"`
	EndNs    uint64 `json:"end_ns"`
	Running  bool   `json:"running"`
}

// toExperimentResult projects a proto Experiment to the tool echo. running is
// derived from end_ns == 0 (the proto's "0 while running" convention).
func toExperimentResult(e *gantryv1.Experiment) experimentResult {
	return experimentResult{
		ID:       e.Id,
		Name:     e.Name,
		Notes:    e.Notes,
		DeviceID: e.DeviceId,
		StartNs:  e.StartNs,
		EndNs:    e.EndNs,
		Running:  e.EndNs == 0,
	}
}

// ---- start_experiment ----

type startExperimentArgs struct {
	Name     string `json:"name" jsonschema:"experiment name (required)"`
	Notes    string `json:"notes,omitempty" jsonschema:"optional free-text notes"`
	DeviceID string `json:"device_id,omitempty" jsonschema:"optional device id to scope the experiment to; omit for bench-wide (all devices)"`
}

func (d Deps) startExperiment(ctx context.Context, req *mcpsdk.CallToolRequest, args startExperimentArgs) (*mcpsdk.CallToolResult, experimentResult, error) {
	if err := requireToolScope(req, auth.ScopeOperate); err != nil {
		return nil, experimentResult{}, err
	}
	if args.Name == "" {
		return nil, experimentResult{}, fmt.Errorf("name is required")
	}
	e, err := d.Experiments.Start(ctx, args.Name, args.Notes, args.DeviceID, 0)
	if err != nil {
		return nil, experimentResult{}, err
	}
	return nil, toExperimentResult(e), nil
}

// ---- stop_experiment ----

type stopExperimentArgs struct {
	ID string `json:"id" jsonschema:"id of the running experiment to stop (from start_experiment or list_experiments)"`
}

func (d Deps) stopExperiment(ctx context.Context, req *mcpsdk.CallToolRequest, args stopExperimentArgs) (*mcpsdk.CallToolResult, experimentResult, error) {
	if err := requireToolScope(req, auth.ScopeOperate); err != nil {
		return nil, experimentResult{}, err
	}
	if args.ID == "" {
		return nil, experimentResult{}, fmt.Errorf("id is required")
	}
	e, err := d.Experiments.Stop(ctx, args.ID, 0)
	if err != nil {
		return nil, experimentResult{}, err
	}
	return nil, toExperimentResult(e), nil
}

// ---- list_experiments ----

type listExperimentsArgs struct {
	DeviceID string `json:"device_id,omitempty" jsonschema:"optional device id filter; omit for all devices"`
}

type listExperimentsResult struct {
	Experiments []experimentResult `json:"experiments"`
}

func (d Deps) listExperiments(ctx context.Context, _ *mcpsdk.CallToolRequest, args listExperimentsArgs) (*mcpsdk.CallToolResult, listExperimentsResult, error) {
	list, err := d.Experiments.List(ctx, args.DeviceID)
	if err != nil {
		return nil, listExperimentsResult{}, err
	}
	out := listExperimentsResult{Experiments: make([]experimentResult, 0, len(list))}
	for _, e := range list {
		out.Experiments = append(out.Experiments, toExperimentResult(e))
	}
	return nil, out, nil
}
