package auth

import "github.com/qleelulu/procmesh/internal/errcode"

func (s *Service) Allow(p Principal, perm, targetNodeID string) error {
	st, err := s.storeOrErr()
	if err != nil {
		return err
	}
	view := st.View()
	if !view.Check(p.UserID, perm, targetNodeID) {
		return errcode.E(errcode.DENIED, "permission denied")
	}
	return nil
}

func (s *Service) AllowWrite(p Principal, perm, targetNodeID string, local bool) error {
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
