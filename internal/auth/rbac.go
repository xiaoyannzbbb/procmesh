package auth

import "github.com/qleelulu/procmesh/internal/errcode"

func (s *Service) Allow(p Principal, perm, targetNodeID string) error {
	st := s.Store.View()
	if !st.Check(p.UserID, perm, targetNodeID) {
		return errcode.E(errcode.DENIED, "permission denied")
	}
	return nil
}

func (s *Service) AllowWrite(p Principal, perm, targetNodeID string) error {
	if isControlPlaneWrite(perm) && !s.Store.HasQuorum() {
		return errcode.E(errcode.UNAVAILABLE, "control quorum lost")
	}
	if isMutation(perm) && !s.Store.HasQuorum() {
		st := s.Store.View()
		if !s.Store.CacheFresh(st.Policy.RBACCacheTTL) {
			return errcode.E(errcode.DENIED, "rbac cache expired")
		}
	}
	return s.Allow(p, perm, targetNodeID)
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
