package experiments

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// Handler adapts a Service to the generated ExperimentService ConnectRPC
// interface, translating domain errors into Connect status codes.
type Handler struct {
	gantryv1connect.UnimplementedExperimentServiceHandler
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
	case errors.Is(err, ErrNotRunning):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func (h *Handler) StartExperiment(ctx context.Context, req *connect.Request[gantryv1.StartExperimentRequest]) (*connect.Response[gantryv1.StartExperimentResponse], error) {
	e, err := h.svc.Start(ctx, req.Msg.Name, req.Msg.Notes, req.Msg.DeviceId, req.Msg.StartNs)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.StartExperimentResponse{Experiment: e}), nil
}

func (h *Handler) StopExperiment(ctx context.Context, req *connect.Request[gantryv1.StopExperimentRequest]) (*connect.Response[gantryv1.StopExperimentResponse], error) {
	e, err := h.svc.Stop(ctx, req.Msg.Id, req.Msg.EndNs)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.StopExperimentResponse{Experiment: e}), nil
}

func (h *Handler) ListExperiments(ctx context.Context, req *connect.Request[gantryv1.ListExperimentsRequest]) (*connect.Response[gantryv1.ListExperimentsResponse], error) {
	list, err := h.svc.List(ctx, req.Msg.DeviceId)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.ListExperimentsResponse{Experiments: list}), nil
}

func (h *Handler) UpdateExperiment(ctx context.Context, req *connect.Request[gantryv1.UpdateExperimentRequest]) (*connect.Response[gantryv1.UpdateExperimentResponse], error) {
	e, err := h.svc.Update(ctx, req.Msg.Id, req.Msg.Name, req.Msg.Notes)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.UpdateExperimentResponse{Experiment: e}), nil
}

func (h *Handler) DeleteExperiment(ctx context.Context, req *connect.Request[gantryv1.DeleteExperimentRequest]) (*connect.Response[gantryv1.DeleteExperimentResponse], error) {
	if err := h.svc.Delete(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.DeleteExperimentResponse{}), nil
}
