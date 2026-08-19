package agent

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/control"
)

func TestAgentBackupSchedulerResolvesFrozenTargets(t *testing.T) {
	st := *control.NewState()
	st.Members["node-b"] = control.Member{NodeID: "node-b", Status: control.MemberAdmitted}
	st.Members["node-a"] = control.Member{NodeID: "node-a", Status: control.MemberAdmitted}
	got := resolveBackupTargets(st, control.BackupPolicy{TargetSelector: "ALL_ADMITTED"})
	if len(got) != 2 || got[0] != "node-a" || got[1] != "node-b" {
		t.Fatalf("targets=%v", got)
	}
	got = resolveBackupTargets(st, control.BackupPolicy{TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"node-b"}})
	if len(got) != 1 || got[0] != "node-b" {
		t.Fatalf("explicit targets=%v", got)
	}
}
