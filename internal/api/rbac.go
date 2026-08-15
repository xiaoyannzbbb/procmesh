package api

import (
	"context"

	"github.com/qleelulu/procmesh/internal/auth"
)

// requirePerm 在 svc==nil 或无 Principal 时放行（旧单测 / 未入群）。
func requirePerm(ctx context.Context, svc *auth.Service, perm, targetNode string, write bool) error {
	if svc == nil {
		return nil
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil
	}
	var err error
	if write {
		err = svc.AllowWrite(p, perm, targetNode)
	} else {
		err = svc.Allow(p, perm, targetNode)
	}
	if err != nil {
		return ToConnect(err)
	}
	return nil
}

func hopTarget(local bool, rt Route, localID string) string {
	if !local {
		return rt.NodeID
	}
	return localID
}
