package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/control"
)

type raftBackupControl struct{ runtime *rpcRuntime }

func (a raftBackupControl) node() *control.Node {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.control()
}

func (a raftBackupControl) ListEnabledBackupPolicies(_ context.Context) ([]backup.PolicyView, error) {
	n := a.node()
	if n == nil {
		return nil, nil
	}
	st := n.View()
	ids := make([]string, 0, len(st.BackupPolicies))
	for id, p := range st.BackupPolicies {
		if p.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]backup.PolicyView, 0, len(ids))
	for _, id := range ids {
		p := st.BackupPolicies[id]
		targets := resolveBackupTargets(st, p)
		out = append(out, backup.PolicyView{Policy: backup.PolicyFromRecord(backup.PolicyRecord{
			PolicyID: p.PolicyID, Name: p.Name, Enabled: p.Enabled, ScheduleCron: p.ScheduleCron, Timezone: p.Timezone,
			TargetSelector: p.TargetSelector, TargetIDs: p.TargetIDs, Sink: p.Sink, DestinationProfile: p.DestinationProfile,
			RetentionKeepLast: p.RetentionKeepLast, RetentionKeepDays: p.RetentionKeepDays, RetentionMaxBytes: p.RetentionMaxBytes,
			TimeoutSeconds: p.TimeoutSeconds, MaxConcurrency: p.MaxConcurrency, UnavailablePolicy: p.UnavailablePolicy, Revision: p.Revision,
		}), Revision: p.Revision, TargetNodeIDs: targets})
	}
	return out, nil
}

func (a raftBackupControl) ClaimFire(ctx context.Context, key, policyID string, term uint64, now time.Time) (string, bool, error) {
	n := a.node()
	if n == nil {
		return "", false, fmt.Errorf("control plane unavailable")
	}
	record, claimed, err := n.ClaimBackupFire(control.FireClaimBody{OperationID: "scheduler-" + key, FireKey: key, PolicyID: policyID, ScheduledUnix: scheduledUnix(key), LeaderTerm: term}, now)
	if err != nil {
		return "", false, err
	}
	return record.RunID, claimed, nil
}

func (a raftBackupControl) UpdateTask(ctx context.Context, u backup.TaskUpdate) error {
	n := a.node()
	if n == nil {
		return fmt.Errorf("control plane unavailable")
	}
	term := n.CurrentTerm()
	cmd, err := control.EncodeCommand(control.CmdBackupTaskUpdate, control.UpdateTaskBody{OperationID: "scheduler-" + u.RunID + ":" + u.TaskID, LeaderTerm: term, Task: control.ClusterBackupTask{RunID: u.RunID, TaskID: u.TaskID, NodeID: u.NodeID, Status: u.Status, SnapshotID: u.SnapshotID, SHA256: u.SHA256, Bytes: u.Bytes, ErrorCode: u.ErrorCode, ErrorSummary: u.ErrorSummary, UpdatedUnix: time.Now().Unix()}})
	if err != nil {
		return err
	}
	return n.Apply(cmd, 5*time.Second)
}

type raftBackupRunCreator struct{ runtime *rpcRuntime }

func (a raftBackupRunCreator) CreateBackupRun(_ context.Context, r backup.BackupRunRequest) error {
	n := a.runtime.control()
	if n == nil {
		return fmt.Errorf("control plane unavailable")
	}
	cmd, err := control.EncodeCommand(control.CmdBackupRunCreate, control.CreateRunBody{OperationID: "scheduler-run-" + r.RunID, LeaderTerm: r.Term, Run: control.ClusterBackupRun{RunID: r.RunID, PolicyID: r.PolicyID, PolicyRevision: r.PolicyRevision, TargetNodeIDs: append([]string(nil), r.TargetNodeIDs...), Status: "RUNNING", CreatedUnix: r.CreatedAt.Unix(), StartedUnix: r.CreatedAt.Unix()}})
	if err != nil {
		return err
	}
	return n.Apply(cmd, 5*time.Second)
}

type localBackupDispatcher struct{ runtime *rpcRuntime }

func (d localBackupDispatcher) DispatchBackupTask(ctx context.Context, task backup.BackupTaskRequest) error {
	if d.runtime == nil || d.runtime.backup == nil {
		return fmt.Errorf("backup engine unavailable")
	}
	if task.NodeID != d.runtime.nodeID {
		return fmt.Errorf("remote agent dispatch unavailable")
	}
	_, err := d.runtime.backup.Create(ctx, backup.CreateOpts{Sink: task.Sink})
	return err
}

func resolveBackupTargets(st control.State, p control.BackupPolicy) []string {
	if p.TargetSelector == "EXPLICIT_NODES" {
		return append([]string(nil), p.TargetIDs...)
	}
	if p.TargetSelector == "AGENT_GROUP" {
		seen := map[string]bool{}
		for _, gid := range p.TargetIDs {
			for _, id := range st.AgentGroups[gid].MemberIDs {
				if m, ok := st.Members[id]; ok && m.Status == control.MemberAdmitted {
					seen[id] = true
				}
			}
		}
		return sortedKeys(seen)
	}
	seen := map[string]bool{}
	for id, m := range st.Members {
		if m.Status == control.MemberAdmitted {
			seen[id] = true
		}
	}
	return sortedKeys(seen)
}
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func scheduledUnix(key string) int64 {
	var n int64
	_, _ = fmt.Sscanf(key[strings.LastIndex(key, ":")+1:], "%d", &n)
	return n
}
