package workspace

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// Handler adapts a Service to the generated WorkspaceService ConnectRPC
// interface, translating domain errors into Connect status codes.
type Handler struct {
	gantryv1connect.UnimplementedWorkspaceServiceHandler
	svc *Service
}

// NewHandler builds the ConnectRPC handler for a Service.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// connectErr maps a domain error to the appropriate Connect code.
func connectErr(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func (h *Handler) ListWorkspaces(ctx context.Context, _ *connect.Request[gantryv1.ListWorkspacesRequest]) (*connect.Response[gantryv1.ListWorkspacesResponse], error) {
	rows, err := h.svc.List(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.ListWorkspacesResponse{Workspaces: rows}), nil
}

func (h *Handler) GetWorkspace(ctx context.Context, req *connect.Request[gantryv1.GetWorkspaceRequest]) (*connect.Response[gantryv1.GetWorkspaceResponse], error) {
	ws, err := h.svc.Get(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.GetWorkspaceResponse{Workspace: ws}), nil
}

func (h *Handler) UpsertWorkspace(ctx context.Context, req *connect.Request[gantryv1.UpsertWorkspaceRequest]) (*connect.Response[gantryv1.UpsertWorkspaceResponse], error) {
	ws, err := h.svc.Upsert(ctx, req.Msg.Workspace)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.UpsertWorkspaceResponse{Workspace: ws}), nil
}

func (h *Handler) DeleteWorkspace(ctx context.Context, req *connect.Request[gantryv1.DeleteWorkspaceRequest]) (*connect.Response[gantryv1.DeleteWorkspaceResponse], error) {
	if err := h.svc.Delete(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.DeleteWorkspaceResponse{}), nil
}
