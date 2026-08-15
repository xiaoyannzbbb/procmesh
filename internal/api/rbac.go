package api

import (
	"context"
	"strings"

	"github.com/qleelulu/procmesh/internal/auth"
)

// requirePerm 在 svc==nil 或无 Principal 时放行（旧单测 / 未入群）。
// local 区分本机 process 读写与远程 Mutation：失 quorum 时本机只做 Allow，远程查 cache TTL。
func requirePerm(ctx context.Context, svc *auth.Service, perm, targetNode string, write, local bool) error {
	if svc == nil {
		return nil
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil
	}
	var err error
	if write {
		err = svc.AllowWrite(p, perm, targetNode, local)
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

func requireRoutePerm(ctx context.Context, svc *auth.Service, perm string, local bool, rt Route, localID string, write bool) error {
	return requirePerm(ctx, svc, perm, hopTarget(local, rt, localID), write, local)
}

func requireHopPerm(ctx context.Context, svc *auth.Service, procedure, localID string) error {
	perm, write, ok := hopRPCPerm(procedure)
	if !ok {
		return nil
	}
	return requirePerm(ctx, svc, perm, localID, write, true)
}

func hopRPCPerm(procedure string) (perm string, write bool, ok bool) {
	name := procedure
	if i := strings.LastIndex(procedure, "/"); i >= 0 {
		name = procedure[i+1:]
	}
	switch name {
	case "ListProcesses", "GetProcess":
		return auth.PermProcessRead, false, true
	case "DeleteProcess":
		return auth.PermProcessDelete, true, true
	case "StartProcess":
		return auth.PermProcessStart, true, true
	case "StopProcess", "KillProcess":
		return auth.PermProcessStop, true, true
	case "RestartProcess":
		return auth.PermProcessRestart, true, true
	case "ResetFailure", "AdoptInstance":
		return auth.PermProcessUpdate, true, true
	case "GetConfig", "History", "Diff":
		return auth.PermProcessConfigRead, false, true
	case "UpdateConfig", "Rollback":
		return auth.PermProcessConfigUpdate, true, true
	case "TailLogs", "StreamLogs":
		return auth.PermProcessLogsRead, false, true
	case "DownloadLogs":
		return auth.PermProcessLogsDownload, false, true
	case "ListAudit":
		return auth.PermAuditRead, false, true
	default:
		// ApplyProcess 的 create/update 由 handler 判定
		return "", false, false
	}
}
