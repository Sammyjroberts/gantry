package server_test

import (
	"context"
	"encoding/json"
	"testing"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPExperimentTools drives the experiment write/read tools over /mcp:
// start_experiment -> stop_experiment -> list_experiments, then confirms the
// same experiment via the ExperimentService ConnectRPC client (proving the MCP
// tools and the RPC service share one persisted engine).
func TestMCPExperimentTools(t *testing.T) {
	baseURL := startEdge(t)
	ctx := context.Background()
	const device = "rover-1"

	mcpClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "gantry-test", Version: "0"}, nil)
	transport := &mcpsdk.StreamableClientTransport{Endpoint: baseURL + "/mcp"}
	session, err := mcpClient.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("MCP Connect: %v", err)
	}
	defer session.Close()

	// The experiment tools must be advertised now that Bench wires an engine.
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"start_experiment", "stop_experiment", "list_experiments"} {
		if !names[want] {
			t.Fatalf("missing experiment tool %q; got %v", want, names)
		}
	}

	type experiment struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		StartNs uint64 `json:"start_ns"`
		EndNs   uint64 `json:"end_ns"`
		Running bool   `json:"running"`
	}
	callExp := func(name string, args map[string]any) experiment {
		res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var e experiment
		if err := json.Unmarshal([]byte(toolText(t, res)), &e); err != nil {
			t.Fatalf("%s unmarshal: %v", name, err)
		}
		return e
	}

	// start_experiment -> running, end_ns 0.
	started := callExp("start_experiment", map[string]any{"name": "Climb Test #7", "device_id": device})
	if started.ID == "" || started.Name != "Climb Test #7" || !started.Running || started.EndNs != 0 {
		t.Fatalf("start_experiment result = %+v", started)
	}

	// stop_experiment -> not running, end_ns set.
	stopped := callExp("stop_experiment", map[string]any{"id": started.ID})
	if stopped.ID != started.ID || stopped.Running || stopped.EndNs == 0 {
		t.Fatalf("stop_experiment result = %+v", stopped)
	}
	if stopped.EndNs <= stopped.StartNs {
		t.Fatalf("stopped end_ns %d not after start_ns %d", stopped.EndNs, stopped.StartNs)
	}

	// list_experiments (via MCP) contains it.
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_experiments", Arguments: map[string]any{"device_id": device}})
	if err != nil {
		t.Fatalf("list_experiments: %v", err)
	}
	var listed struct {
		Experiments []experiment `json:"experiments"`
	}
	if err := json.Unmarshal([]byte(toolText(t, res)), &listed); err != nil {
		t.Fatalf("list unmarshal: %v", err)
	}
	found := false
	for _, e := range listed.Experiments {
		if e.ID == started.ID {
			found = true
			if e.Running {
				t.Errorf("listed experiment still running: %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("started experiment %s not in MCP list: %+v", started.ID, listed.Experiments)
	}

	// Cross-check via the ExperimentService ConnectRPC client: same id present.
	expClient := gantryv1connect.NewExperimentServiceClient(h2cClient(), baseURL)
	rpcList, err := expClient.ListExperiments(ctx, connect.NewRequest(&gantryv1.ListExperimentsRequest{DeviceId: device}))
	if err != nil {
		t.Fatalf("ExperimentService.ListExperiments: %v", err)
	}
	rpcFound := false
	for _, e := range rpcList.Msg.Experiments {
		if e.Id == started.ID && e.Name == "Climb Test #7" && e.EndNs != 0 {
			rpcFound = true
		}
	}
	if !rpcFound {
		t.Fatalf("experiment %s not confirmed via ExperimentService client: %v", started.ID, rpcList.Msg.Experiments)
	}
}
