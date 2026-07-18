package source

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// Handler adapts a Service + Supervisor to the generated SourceService
// ConnectRPC interface. Reads combine the persisted rows (Service) with the
// supervisor's live status; writes persist then reconcile the supervisor so a
// toggle takes effect promptly.
type Handler struct {
	gantryv1connect.UnimplementedSourceServiceHandler
	svc *Service
	sup *Supervisor
}

// NewHandler builds the ConnectRPC handler over a Service and its Supervisor.
func NewHandler(svc *Service, sup *Supervisor) *Handler {
	return &Handler{svc: svc, sup: sup}
}

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

func (h *Handler) ListSources(ctx context.Context, _ *connect.Request[gantryv1.ListSourcesRequest]) (*connect.Response[gantryv1.ListSourcesResponse], error) {
	rows, err := h.svc.List(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.ListSourcesResponse{
		Sources:  rows,
		Statuses: h.sup.StatusFor(rows),
	}), nil
}

func (h *Handler) UpsertSource(ctx context.Context, req *connect.Request[gantryv1.UpsertSourceRequest]) (*connect.Response[gantryv1.UpsertSourceResponse], error) {
	src, err := h.svc.Upsert(ctx, req.Msg.Source)
	if err != nil {
		return nil, connectErr(err)
	}
	// Reconcile on a background context: the persisted change must take effect
	// even after this request returns (the runner goroutines outlive it).
	if err := h.sup.Reconcile(context.Background()); err != nil {
		h.sup.logf("source: reconcile after upsert %s: %v", src.Id, err)
	}
	return connect.NewResponse(&gantryv1.UpsertSourceResponse{Source: src}), nil
}

func (h *Handler) DeleteSource(ctx context.Context, req *connect.Request[gantryv1.DeleteSourceRequest]) (*connect.Response[gantryv1.DeleteSourceResponse], error) {
	if err := h.svc.Delete(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	if err := h.sup.Reconcile(context.Background()); err != nil {
		h.sup.logf("source: reconcile after delete %s: %v", req.Msg.Id, err)
	}
	return connect.NewResponse(&gantryv1.DeleteSourceResponse{}), nil
}
