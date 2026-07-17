package auth

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// Handler adapts a Service to the generated TokenService ConnectRPC interface.
// It only translates domain errors to status codes — the ACTUAL authorization
// (only loopback or an admin token may manage tokens) is enforced upstream by
// Middleware via the route-family map (TokenService/* → admin). Keeping the
// authz in the middleware means there is one place to audit it, and this handler
// stays a thin CRUD adapter like workspace/hardware.
type Handler struct {
	gantryv1connect.UnimplementedTokenServiceHandler
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

func (h *Handler) ListTokens(ctx context.Context, _ *connect.Request[gantryv1.ListTokensRequest]) (*connect.Response[gantryv1.ListTokensResponse], error) {
	tokens, err := h.svc.List(ctx)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.ListTokensResponse{Tokens: tokens}), nil
}

func (h *Handler) CreateToken(ctx context.Context, req *connect.Request[gantryv1.CreateTokenRequest]) (*connect.Response[gantryv1.CreateTokenResponse], error) {
	info, secret, err := h.svc.Create(ctx, req.Msg.Name, req.Msg.Scopes)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.CreateTokenResponse{Token: info, Secret: secret}), nil
}

func (h *Handler) DeleteToken(ctx context.Context, req *connect.Request[gantryv1.DeleteTokenRequest]) (*connect.Response[gantryv1.DeleteTokenResponse], error) {
	if err := h.svc.Delete(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&gantryv1.DeleteTokenResponse{}), nil
}
