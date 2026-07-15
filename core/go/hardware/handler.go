package hardware

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// Handler adapts a Service to the generated HardwareService ConnectRPC
// interface, translating domain errors into Connect status codes.
type Handler struct {
	gantryv1connect.UnimplementedHardwareServiceHandler
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

func (h *Handler) ListHardware(ctx context.Context, _ *connect.Request[gantryv1.ListHardwareRequest]) (*connect.Response[gantryv1.ListHardwareResponse], error) {
	rows, unconfigured, err := h.svc.List(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.ListHardwareResponse{
		Hardware:              rows,
		UnconfiguredDeviceIds: unconfigured,
	}), nil
}

func (h *Handler) GetHardware(ctx context.Context, req *connect.Request[gantryv1.GetHardwareRequest]) (*connect.Response[gantryv1.GetHardwareResponse], error) {
	hw, err := h.svc.Get(ctx, req.Msg.DeviceId)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.GetHardwareResponse{Hardware: hw}), nil
}

func (h *Handler) UpsertHardware(ctx context.Context, req *connect.Request[gantryv1.UpsertHardwareRequest]) (*connect.Response[gantryv1.UpsertHardwareResponse], error) {
	hw, err := h.svc.Upsert(ctx, req.Msg.Hardware)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.UpsertHardwareResponse{Hardware: hw}), nil
}

func (h *Handler) DeleteHardware(ctx context.Context, req *connect.Request[gantryv1.DeleteHardwareRequest]) (*connect.Response[gantryv1.DeleteHardwareResponse], error) {
	if err := h.svc.Delete(ctx, req.Msg.DeviceId); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.DeleteHardwareResponse{}), nil
}
