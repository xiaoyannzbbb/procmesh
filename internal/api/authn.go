package api

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type principalKey struct{}

func WithPrincipal(ctx context.Context, p auth.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFrom(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	return p, ok
}

func AuthInterceptor(svc *auth.Service, clusterInited func() bool) connect.Interceptor {
	return &authInterceptor{svc: svc, inited: clusterInited}
}

type authInterceptor struct {
	svc    *auth.Service
	inited func() bool
}

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := a.authenticate(ctx, req.Spec().Procedure, req.Header())
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := a.authenticate(ctx, conn.Spec().Procedure, conn.RequestHeader())
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (a *authInterceptor) authenticate(ctx context.Context, procedure string, h http.Header) (context.Context, error) {
	if a.svc == nil || a.inited == nil || !a.inited() {
		return ctx, nil
	}
	if isPublicRPC(procedure) {
		return ctx, nil
	}
	p, err := principalFromHeader(a.svc, procedure, h)
	if err != nil {
		return ctx, ToConnect(err)
	}
	return WithPrincipal(ctx, p), nil
}

func principalFromHeader(svc *auth.Service, procedure string, h http.Header) (auth.Principal, error) {
	if tok := bearerToken(h); tok != "" {
		return svc.AuthenticateBearer(tok)
	}
	sid := cookieValue(h, auth.CookieName)
	if sid == "" {
		return auth.Principal{}, errcode.E(errcode.DENIED, "authentication required")
	}
	return svc.AuthenticateSession(sid, h.Get(auth.HeaderCSRF), isMutationRPC(procedure))
}

func isPublicRPC(procedure string) bool {
	return strings.HasSuffix(procedure, procmeshv1connect.AuthServiceLoginProcedure) ||
		strings.HasSuffix(procedure, procmeshv1connect.ClusterServiceJoinProcedure) ||
		strings.HasSuffix(procedure, procmeshv1connect.ClusterServiceRequestJoinProcedure)
}

func isMutationRPC(procedure string) bool {
	name := procedure
	if i := strings.LastIndex(procedure, "/"); i >= 0 {
		name = procedure[i+1:]
	}
	for _, p := range []string{"List", "Get", "Overview", "History", "Diff", "Tail", "Stream", "Download", "Status"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

func bearerToken(h http.Header) string {
	v := h.Get("Authorization")
	const prefix = "Bearer "
	if len(v) >= len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		return strings.TrimSpace(v[len(prefix):])
	}
	return ""
}

func cookieValue(h http.Header, name string) string {
	c, err := (&http.Request{Header: h}).Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}
