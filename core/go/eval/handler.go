package eval

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// Handler adapts a Service to the generated EvalService ConnectRPC interface,
// translating domain errors into Connect status codes.
type Handler struct {
	gantryv1connect.UnimplementedEvalServiceHandler
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

func (h *Handler) UpsertSuite(ctx context.Context, req *connect.Request[gantryv1.UpsertSuiteRequest]) (*connect.Response[gantryv1.UpsertSuiteResponse], error) {
	su, err := h.svc.UpsertSuite(ctx, req.Msg.Suite)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.UpsertSuiteResponse{Suite: su}), nil
}

func (h *Handler) ListSuites(ctx context.Context, req *connect.Request[gantryv1.ListSuitesRequest]) (*connect.Response[gantryv1.ListSuitesResponse], error) {
	list, err := h.svc.ListSuites(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.ListSuitesResponse{Suites: list}), nil
}

func (h *Handler) GetSuite(ctx context.Context, req *connect.Request[gantryv1.GetSuiteRequest]) (*connect.Response[gantryv1.GetSuiteResponse], error) {
	su, err := h.svc.GetSuite(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.GetSuiteResponse{Suite: su}), nil
}

func (h *Handler) RegisterSubject(ctx context.Context, req *connect.Request[gantryv1.RegisterSubjectRequest]) (*connect.Response[gantryv1.RegisterSubjectResponse], error) {
	sub, err := h.svc.RegisterSubject(ctx, req.Msg.Subject)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.RegisterSubjectResponse{Subject: sub}), nil
}

func (h *Handler) StartRun(ctx context.Context, req *connect.Request[gantryv1.StartRunRequest]) (*connect.Response[gantryv1.StartRunResponse], error) {
	run, err := h.svc.StartRun(ctx, req.Msg.SuiteId, req.Msg.Candidate, req.Msg.BaselineRef, req.Msg.TargetSelector, req.Msg.Replicas, req.Msg.IdempotencyKey)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.StartRunResponse{Run: run}), nil
}

func (h *Handler) ListRuns(ctx context.Context, req *connect.Request[gantryv1.ListRunsRequest]) (*connect.Response[gantryv1.ListRunsResponse], error) {
	list, err := h.svc.ListRuns(ctx, req.Msg.SuiteId)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.ListRunsResponse{Runs: list}), nil
}

func (h *Handler) GetRun(ctx context.Context, req *connect.Request[gantryv1.GetRunRequest]) (*connect.Response[gantryv1.GetRunResponse], error) {
	run, trials, err := h.svc.GetRun(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.GetRunResponse{Run: run, Trials: trials}), nil
}

func (h *Handler) OpenTrial(ctx context.Context, req *connect.Request[gantryv1.OpenTrialRequest]) (*connect.Response[gantryv1.OpenTrialResponse], error) {
	t, err := h.svc.OpenTrial(ctx, req.Msg.RunId, req.Msg.ScenarioId, req.Msg.Attempt, req.Msg.StationId, req.Msg.Seed)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.OpenTrialResponse{Trial: t}), nil
}

func (h *Handler) CloseTrial(ctx context.Context, req *connect.Request[gantryv1.CloseTrialRequest]) (*connect.Response[gantryv1.CloseTrialResponse], error) {
	t, err := h.svc.CloseTrial(ctx, req.Msg.TrialId, req.Msg.EndNs, req.Msg.VideoChunkIds)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.CloseTrialResponse{Trial: t}), nil
}

func (h *Handler) SubmitVerdict(ctx context.Context, req *connect.Request[gantryv1.SubmitVerdictRequest]) (*connect.Response[gantryv1.SubmitVerdictResponse], error) {
	t, err := h.svc.SubmitVerdict(ctx, req.Msg.TrialId, req.Msg.Verdict)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.SubmitVerdictResponse{Trial: t}), nil
}
