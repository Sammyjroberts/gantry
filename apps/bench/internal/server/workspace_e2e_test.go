package server_test

import (
	"context"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// TestWorkspaceCRUDFlow drives the full slice over the wired WorkspaceService:
// create (server generates an id) → list (layout omitted) → get (layout present)
// → update → delete. Proves the handler is mounted in server.New.
func TestWorkspaceCRUDFlow(t *testing.T) {
	baseURL := startEdge(t)
	ws := gantryv1connect.NewWorkspaceServiceClient(h2cClient(), baseURL)
	ctx := context.Background()

	const layout = `{"v":1,"panels":[{"id":"p1","type":"chart"}]}`

	// Create with empty id → server generates one.
	up, err := ws.UpsertWorkspace(ctx, connect.NewRequest(&gantryv1.UpsertWorkspaceRequest{
		Workspace: &gantryv1.Workspace{Name: "Drive Dashboard", LayoutJson: layout},
	}))
	if err != nil {
		t.Fatalf("UpsertWorkspace create: %v", err)
	}
	id := up.Msg.Workspace.Id
	if id == "" {
		t.Fatalf("server did not generate an id: %+v", up.Msg.Workspace)
	}
	if up.Msg.Workspace.CreatedNs == 0 || up.Msg.Workspace.UpdatedNs == 0 {
		t.Fatalf("timestamps not stamped: %+v", up.Msg.Workspace)
	}

	// List returns the row WITHOUT layout_json.
	list, err := ws.ListWorkspaces(ctx, connect.NewRequest(&gantryv1.ListWorkspacesRequest{}))
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list.Msg.Workspaces) != 1 || list.Msg.Workspaces[0].Id != id {
		t.Fatalf("List = %+v", list.Msg.Workspaces)
	}
	if list.Msg.Workspaces[0].LayoutJson != "" {
		t.Fatalf("List must omit layout_json, got %q", list.Msg.Workspaces[0].LayoutJson)
	}
	if list.Msg.Workspaces[0].Name != "Drive Dashboard" {
		t.Fatalf("List name = %q", list.Msg.Workspaces[0].Name)
	}

	// Get returns the full layout.
	got, err := ws.GetWorkspace(ctx, connect.NewRequest(&gantryv1.GetWorkspaceRequest{Id: id}))
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.Msg.Workspace.LayoutJson != layout {
		t.Fatalf("Get layout = %q, want %q", got.Msg.Workspace.LayoutJson, layout)
	}

	// Update: same id, new name; created_ns preserved.
	up2, err := ws.UpsertWorkspace(ctx, connect.NewRequest(&gantryv1.UpsertWorkspaceRequest{
		Workspace: &gantryv1.Workspace{Id: id, Name: "Drive Dashboard v2", LayoutJson: layout},
	}))
	if err != nil {
		t.Fatalf("UpsertWorkspace update: %v", err)
	}
	if up2.Msg.Workspace.CreatedNs != up.Msg.Workspace.CreatedNs {
		t.Fatalf("created_ns changed on update: %d -> %d", up.Msg.Workspace.CreatedNs, up2.Msg.Workspace.CreatedNs)
	}
	if up2.Msg.Workspace.Name != "Drive Dashboard v2" {
		t.Fatalf("update not applied: %+v", up2.Msg.Workspace)
	}

	// Invalid: empty name → InvalidArgument.
	_, err = ws.UpsertWorkspace(ctx, connect.NewRequest(&gantryv1.UpsertWorkspaceRequest{
		Workspace: &gantryv1.Workspace{Name: ""},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty name err = %v, want InvalidArgument", err)
	}

	// Delete, then Get → NotFound.
	if _, err := ws.DeleteWorkspace(ctx, connect.NewRequest(&gantryv1.DeleteWorkspaceRequest{Id: id})); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	_, err = ws.GetWorkspace(ctx, connect.NewRequest(&gantryv1.GetWorkspaceRequest{Id: id}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Get after delete err = %v, want NotFound", err)
	}
}

// TestWorkspaceSurvivesRestart proves persistence: a workspace saved against one
// Bench instance is still present, with its full layout, after the process
// restarts on the same data dir (a new App over the same bench.db).
func TestWorkspaceSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	const layout = `{"v":1,"panels":[{"id":"p1","grid":{"x":0,"y":0,"w":6,"h":4}}]}`

	url1, app1 := startEdgeOnDir(t, dir)
	ws1 := gantryv1connect.NewWorkspaceServiceClient(h2cClient(), url1)
	up, err := ws1.UpsertWorkspace(ctx, connect.NewRequest(&gantryv1.UpsertWorkspaceRequest{
		Workspace: &gantryv1.Workspace{Name: "Persisted", LayoutJson: layout},
	}))
	if err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	id := up.Msg.Workspace.Id

	shutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := app1.Shutdown(shutCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	url2, app2 := startEdgeOnDir(t, dir)
	t.Cleanup(func() {
		c, cn := context.WithTimeout(context.Background(), 5*time.Second)
		defer cn()
		_ = app2.Shutdown(c)
	})
	ws2 := gantryv1connect.NewWorkspaceServiceClient(h2cClient(), url2)
	got, err := ws2.GetWorkspace(ctx, connect.NewRequest(&gantryv1.GetWorkspaceRequest{Id: id}))
	if err != nil {
		t.Fatalf("GetWorkspace after restart: %v", err)
	}
	if got.Msg.Workspace.Name != "Persisted" || got.Msg.Workspace.LayoutJson != layout {
		t.Fatalf("workspace did not survive restart: %+v", got.Msg.Workspace)
	}
}
