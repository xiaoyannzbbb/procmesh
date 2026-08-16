package auth

import (
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func (s *Service) Allow(p Principal, perm, targetNodeID string) error {
	return s.AllowOn(p, perm, control.CheckTarget{NodeID: targetNodeID})
}

func (s *Service) AllowOn(p Principal, perm string, t control.CheckTarget) error {
	st, err := s.storeOrErr()
	if err != nil {
		return err
	}
	view := st.View()
	if !view.CheckTarget(p.UserID, perm, t) {
		return errcode.E(errcode.DENIED, "permission denied")
	}
	return nil
}

func (s *Service) AllowAny(p Principal, perm string) error {
	st, err := s.storeOrErr()
	if err != nil {
		return err
	}
	view := st.View()
	if !view.CanAny(p.UserID, perm) {
		return errcode.E(errcode.DENIED, "permission denied")
	}
	return nil
}

func (s *Service) AllowWriteOn(p Principal, perm string, t control.CheckTarget, local bool) error {
	st, err := s.storeOrErr()
	if err != nil {
		return err
	}
	if isControlPlaneWrite(perm) && !st.HasQuorum() {
		return errcode.E(errcode.UNAVAILABLE, "control quorum lost")
	}
	if !local && isMutation(perm) && !st.HasQuorum() {
		if !st.CacheFresh(st.View().Policy.RBACCacheTTL) {
			return errcode.E(errcode.DENIED, "rbac cache expired")
		}
	}
	return s.AllowOn(p, perm, t)
}

func (s *Service) AllowWrite(p Principal, perm, targetNodeID string, local bool) error {
	return s.AllowWriteOn(p, perm, control.CheckTarget{NodeID: targetNodeID}, local)
}

func isControlPlaneWrite(perm string) bool {
	switch perm {
	case PermUserCreate, PermUserUpdate, PermUserDelete,
		PermRoleManage, PermNodeRemove, PermNodeManage, PermClusterManage:
		return true
	default:
		return false
	}
}

func isMutation(perm string) bool {
	switch perm {
	case PermClusterRead, PermNodeRead, PermProcessRead,
		PermProcessConfigRead, PermProcessLogsRead, PermProcessLogsDownload,
		PermUserRead, PermRoleRead, PermAuditRead:
		return false
	default:
		return true
	}
}
