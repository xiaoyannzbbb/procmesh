package api

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/rpc"
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

// OwnerAuthInterceptor 在 :9001 再验 hop 会话，不信任入口「已授权」头。
func OwnerAuthInterceptor(svc *auth.Service, localID string) connect.Interceptor {
	return &ownerAuthInterceptor{svc: svc, localID: localID}
}

type ownerAuthInterceptor struct {
	svc     *auth.Service
	localID string
}

func (o *ownerAuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := o.authenticate(ctx, req.Spec().Procedure, req.Header())
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (o *ownerAuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (o *ownerAuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := o.authenticate(ctx, conn.Spec().Procedure, conn.RequestHeader())
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (o *ownerAuthInterceptor) authenticate(ctx context.Context, procedure string, h http.Header) (context.Context, error) {
	if o.svc == nil {
		return ctx, nil
	}
	var (
		p   auth.Principal
		err error
	)
	switch {
	case rpc.SessionIDOf(h) != "":
		p, err = o.svc.AuthenticateSession(rpc.SessionIDOf(h), "", false)
	case rpc.TokenIDOf(h) != "":
		p, err = o.svc.AuthenticateTokenID(rpc.TokenIDOf(h))
	default:
		return ctx, ToConnect(errcode.E(errcode.DENIED, "missing session"))
	}
	if err != nil {
		return ctx, ToConnect(err)
	}
	ctx = WithPrincipal(ctx, p)
	if err := requireHopPerm(ctx, o.svc, procedure, o.localID); err != nil {
		return ctx, err
	}
	return ctx, nil
}
