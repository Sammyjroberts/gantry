package stations

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// Handler adapts a Service to the generated StationService ConnectRPC interface.
type Handler struct {
	gantryv1connect.UnimplementedStationServiceHandler
	svc *Service
}

// NewHandler builds the ConnectRPC handler for a Service.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func connectErr(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ErrUnavailable):
		return connect.NewError(connect.CodeResourceExhausted, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func (h *Handler) RegisterStation(ctx context.Context, req *connect.Request[gantryv1.RegisterStationRequest]) (*connect.Response[gantryv1.RegisterStationResponse], error) {
	st, err := h.svc.Register(ctx, req.Msg.Station)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.RegisterStationResponse{Station: st}), nil
}

func (h *Handler) ListStations(ctx context.Context, req *connect.Request[gantryv1.ListStationsRequest]) (*connect.Response[gantryv1.ListStationsResponse], error) {
	list, err := h.svc.List(ctx, req.Msg.Selector)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.ListStationsResponse{Stations: list}), nil
}

func (h *Handler) GetStation(ctx context.Context, req *connect.Request[gantryv1.GetStationRequest]) (*connect.Response[gantryv1.GetStationResponse], error) {
	st, err := h.svc.Get(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.GetStationResponse{Station: st}), nil
}

func (h *Handler) CheckTarget(ctx context.Context, req *connect.Request[gantryv1.CheckTargetRequest]) (*connect.Response[gantryv1.CheckTargetResponse], error) {
	resp, err := h.svc.CheckTarget(ctx, req.Msg.Selector, req.Msg.Replicas)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) Lease(ctx context.Context, req *connect.Request[gantryv1.LeaseRequest]) (*connect.Response[gantryv1.LeaseResponse], error) {
	leases, stations, err := h.svc.Lease(ctx, req.Msg.Selector, req.Msg.Replicas, req.Msg.Holder, req.Msg.Reason, req.Msg.TtlSeconds, req.Msg.IdempotencyKey)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.LeaseResponse{Leases: leases, Stations: stations}), nil
}

func (h *Handler) RenewLease(ctx context.Context, req *connect.Request[gantryv1.RenewLeaseRequest]) (*connect.Response[gantryv1.RenewLeaseResponse], error) {
	l, err := h.svc.Renew(ctx, req.Msg.LeaseId, req.Msg.TtlSeconds)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.RenewLeaseResponse{Lease: l}), nil
}

func (h *Handler) ReleaseLease(ctx context.Context, req *connect.Request[gantryv1.ReleaseLeaseRequest]) (*connect.Response[gantryv1.ReleaseLeaseResponse], error) {
	if err := h.svc.Release(ctx, req.Msg.LeaseId); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.ReleaseLeaseResponse{}), nil
}
