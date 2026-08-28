package backup

import (
	"context"
	"sort"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

// RetentionSnapshot is the small, non-payload metadata used to plan deletion.
// SourceNodeID scopes keep_last independently for each backup owner.
type RetentionSnapshot struct {
	SnapshotID        string
	PolicyID          string
	SourceNodeID      string
	NodeID            string
	CreatedAt         time.Time
	Bytes             int64
	Status            string
	Active            bool
	LastUsableReplica bool
}

// PlanRetention returns snapshots that may be deleted, oldest first. It never
// selects active snapshots or the last usable replica.
func PlanRetention(now time.Time, policy Policy, snapshots []RetentionSnapshot) ([]RetentionSnapshot, error) {
	if policy.RetentionKeepLast <= 0 && policy.RetentionKeepDays <= 0 && policy.RetentionMaxBytes <= 0 {
		return []RetentionSnapshot{}, nil
	}
	loc := now.Location()
	if policy.Timezone != "" {
		loaded, err := time.LoadLocation(policy.Timezone)
		if err != nil {
			return nil, errcode.E(errcode.INVALID, "invalid retention timezone")
		}
		loc = loaded
	}

	eligible := make([]RetentionSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.PolicyID != policy.PolicyID || !completedRetentionStatus(snapshot.Status) {
			continue
		}
		eligible = append(eligible, snapshot)
	}
	sortRetentionSnapshots(eligible)

	selected := make(map[string]bool, len(eligible))
	protected := func(snapshot RetentionSnapshot) bool {
		return snapshot.Active || snapshot.LastUsableReplica
	}
	selectSnapshot := func(snapshot RetentionSnapshot) {
		if !protected(snapshot) {
			selected[retentionSnapshotKey(snapshot)] = true
		}
	}

	countExpired := make(map[string]bool, len(eligible))
	if policy.RetentionKeepLast > 0 {
		bySource := make(map[string][]RetentionSnapshot)
		for _, snapshot := range eligible {
			bySource[retentionSource(snapshot)] = append(bySource[retentionSource(snapshot)], snapshot)
		}
		for _, group := range bySource {
			deleteCount := len(group) - policy.RetentionKeepLast
			for i := 0; i < deleteCount; i++ {
				countExpired[retentionSnapshotKey(group[i])] = true
			}
		}
	}

	dayExpired := make(map[string]bool, len(eligible))
	if policy.RetentionKeepDays > 0 {
		localNow := now.In(loc)
		startToday := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
		cutoff := startToday.AddDate(0, 0, -(policy.RetentionKeepDays - 1))
		for _, snapshot := range eligible {
			if snapshot.CreatedAt.Before(cutoff) {
				dayExpired[retentionSnapshotKey(snapshot)] = true
			}
		}
	}
	for _, snapshot := range eligible {
		key := retentionSnapshotKey(snapshot)
		expired := false
		switch {
		case policy.RetentionKeepLast > 0 && policy.RetentionKeepDays > 0:
			expired = countExpired[key] && dayExpired[key]
		case policy.RetentionKeepLast > 0:
			expired = countExpired[key]
		case policy.RetentionKeepDays > 0:
			expired = dayExpired[key]
		}
		if expired {
			selectSnapshot(snapshot)
		}
	}

	if policy.RetentionMaxBytes > 0 {
		var retainedBytes int64
		for _, snapshot := range eligible {
			if !selected[retentionSnapshotKey(snapshot)] && snapshot.Bytes > 0 {
				retainedBytes += snapshot.Bytes
			}
		}
		for _, snapshot := range eligible {
			if retainedBytes <= policy.RetentionMaxBytes {
				break
			}
			key := retentionSnapshotKey(snapshot)
			if selected[key] || protected(snapshot) {
				continue
			}
			selected[key] = true
			if snapshot.Bytes > 0 {
				retainedBytes -= snapshot.Bytes
			}
		}
	}

	protectLastRemainingPerSource(eligible, selected)

	out := make([]RetentionSnapshot, 0, len(selected))
	for _, snapshot := range eligible {
		if selected[retentionSnapshotKey(snapshot)] {
			out = append(out, snapshot)
		}
	}
	return out, nil
}

func protectLastRemainingPerSource(eligible []RetentionSnapshot, selected map[string]bool) {
	bySource := make(map[string][]RetentionSnapshot)
	for _, snapshot := range eligible {
		bySource[retentionSource(snapshot)] = append(bySource[retentionSource(snapshot)], snapshot)
	}
	for _, group := range bySource {
		remaining := 0
		for _, snapshot := range group {
			if !selected[retentionSnapshotKey(snapshot)] {
				remaining++
			}
		}
		if remaining > 0 || len(group) == 0 {
			continue
		}
		delete(selected, retentionSnapshotKey(group[len(group)-1]))
	}
}

func completedRetentionStatus(status string) bool {
	return status == "" || status == "SUCCESS" || status == "SUCCEEDED"
}

func retentionSource(snapshot RetentionSnapshot) string {
	if snapshot.SourceNodeID != "" {
		return snapshot.SourceNodeID
	}
	return snapshot.NodeID
}

func retentionSnapshotKey(snapshot RetentionSnapshot) string {
	return retentionSource(snapshot) + "\x00" + snapshot.SnapshotID
}

func sortRetentionSnapshots(snapshots []RetentionSnapshot) {
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].CreatedAt.Equal(snapshots[j].CreatedAt) {
			if retentionSource(snapshots[i]) == retentionSource(snapshots[j]) {
				return snapshots[i].SnapshotID < snapshots[j].SnapshotID
			}
			return retentionSource(snapshots[i]) < retentionSource(snapshots[j])
		}
		return snapshots[i].CreatedAt.Before(snapshots[j].CreatedAt)
	})
}

// RetentionResult represents the outcome of deleting one snapshot during retention.
type RetentionResult struct {
	SnapshotID string
	Status     string // SUCCESS | FAILED
	Error      string
	ErrorCode  string
	Retryable  bool
}

// RetentionDeleteEvent is the ID-only record emitted after each cluster retention delete attempt.
type RetentionDeleteEvent struct {
	PolicyID, RunID, TaskID, SnapshotID, Sink, Status, Error string
}

// Run executes retention policy on snapshots within the policy's namespace/prefix.
// It skips snapshots that are running, restoring, or are the only available replica.
// Returns deletion results for each removed snapshot.
func Run(ctx context.Context, clusterID string, policy Policy, sink ClusterSink) ([]RetentionResult, error) {
	if policy.RetentionKeepLast <= 0 && policy.RetentionKeepDays <= 0 && policy.RetentionMaxBytes <= 0 {
		return nil, nil
	}

	// List snapshots only within this policy's namespace
	listed, err := sink.ListCluster(ctx, clusterID, policy.PolicyID)
	if err != nil {
		return nil, err
	}

	snapshots := make([]RetentionSnapshot, 0, len(listed))
	byID := make(map[string]Listed, len(listed))
	for _, item := range listed {
		item.PolicyID = policy.PolicyID
		item.ClusterID = clusterID
		snapshot := RetentionSnapshot{SnapshotID: item.SnapshotID, PolicyID: policy.PolicyID, SourceNodeID: item.NodeID, NodeID: item.NodeID, CreatedAt: item.CreatedAt, Bytes: item.Bytes, Status: "SUCCEEDED"}
		snapshots = append(snapshots, snapshot)
		byID[retentionSnapshotKey(snapshot)] = item
	}
	planned, err := PlanRetention(time.Now(), policy, snapshots)
	if err != nil {
		return nil, err
	}
	var results []RetentionResult
	for _, snapshot := range planned {
		item := byID[retentionSnapshotKey(snapshot)]
		err := sink.DeleteCluster(ctx, clusterID, policy.PolicyID, item.NodeID, snapshot.SnapshotID)
		if err != nil {
			results = append(results, RetentionResult{
				SnapshotID: snapshot.SnapshotID,
				Status:     "RETENTION_FAILED",
				Error:      err.Error(),
				ErrorCode:  "RETENTION_DELETE_FAILED",
				Retryable:  true,
			})
		} else {
			results = append(results, RetentionResult{
				SnapshotID: snapshot.SnapshotID,
				Status:     "SUCCESS",
				Error:      "",
			})
		}
	}

	return results, nil
}

// ApplyRetention plans from the local backup index, deletes exact cluster
// objects through the selected sink, and removes index rows only after the
// object is gone. Failed deletions remain indexed for a retry.
func (e *Engine) ApplyRetention(ctx context.Context, policy Policy) ([]RetentionResult, error) {
	if e == nil || e.Store == nil {
		return nil, errcode.E(errcode.INVALID, "backup store unavailable")
	}
	if policy.RetentionKeepLast <= 0 && policy.RetentionKeepDays <= 0 && policy.RetentionMaxBytes <= 0 {
		return []RetentionResult{}, nil
	}
	records, err := e.Store.ListBackups(ctx)
	if err != nil {
		return nil, err
	}
	snapshots := make([]RetentionSnapshot, 0, len(records))
	byID := make(map[string]store.BackupRecord, len(records))
	for _, record := range records {
		if record.ClusterID != e.ClusterID || record.PolicyID != policy.PolicyID || record.Sink != policy.Sink {
			continue
		}
		snapshot := RetentionSnapshot{SnapshotID: record.SnapshotID, PolicyID: record.PolicyID, SourceNodeID: record.NodeID, NodeID: record.NodeID, CreatedAt: record.CreatedAt, Bytes: record.Bytes, Status: "SUCCEEDED"}
		snapshot.Active = e.retentionActive(record.SnapshotID)
		if e.LastUsableReplica != nil {
			snapshot.LastUsableReplica = e.LastUsableReplica(record.SnapshotID)
		}
		snapshots = append(snapshots, snapshot)
		byID[record.SnapshotID] = record
	}
	planned, err := PlanRetention(e.now(), policy, snapshots)
	if err != nil {
		return nil, err
	}
	results := make([]RetentionResult, 0, len(planned))
	for _, snapshot := range planned {
		record := byID[snapshot.SnapshotID]
		sink, sinkErr := e.clusterSink(record.Sink, record.DestinationProfile)
		if sinkErr != nil {
			results = e.appendRetentionResult(ctx, policy, record, results, RetentionResult{SnapshotID: record.SnapshotID, Status: "RETENTION_FAILED", Error: sinkErr.Error(), ErrorCode: "RETENTION_SINK_FAILED", Retryable: true})
			continue
		}
		clusterSink, ok := sink.(ClusterSink)
		if !ok {
			results = e.appendRetentionResult(ctx, policy, record, results, RetentionResult{SnapshotID: record.SnapshotID, Status: "RETENTION_FAILED", Error: "sink does not support cluster retention", ErrorCode: "RETENTION_SINK_FAILED", Retryable: true})
			continue
		}
		deleteErr := clusterSink.DeleteCluster(ctx, record.ClusterID, record.PolicyID, record.NodeID, record.SnapshotID)
		if deleteErr != nil && !errcode.Is(deleteErr, errcode.NOT_FOUND) {
			results = e.appendRetentionResult(ctx, policy, record, results, RetentionResult{SnapshotID: record.SnapshotID, Status: "RETENTION_FAILED", Error: deleteErr.Error(), ErrorCode: "RETENTION_DELETE_FAILED", Retryable: true})
			continue
		}
		if err := e.Store.DeleteBackup(ctx, record.SnapshotID); err != nil && !errcode.Is(err, errcode.NOT_FOUND) {
			results = e.appendRetentionResult(ctx, policy, record, results, RetentionResult{SnapshotID: record.SnapshotID, Status: "RETENTION_FAILED", Error: err.Error(), ErrorCode: "RETENTION_INDEX_FAILED", Retryable: true})
			continue
		}
		results = e.appendRetentionResult(ctx, policy, record, results, RetentionResult{SnapshotID: record.SnapshotID, Status: "SUCCESS"})
	}
	return results, nil
}

func (e *Engine) appendRetentionResult(ctx context.Context, policy Policy, record store.BackupRecord, results []RetentionResult, result RetentionResult) []RetentionResult {
	results = append(results, result)
	if e == nil || e.OnRetentionDelete == nil {
		return results
	}
	e.OnRetentionDelete(ctx, RetentionDeleteEvent{
		PolicyID: policy.PolicyID, RunID: record.RunID, TaskID: record.TaskID,
		SnapshotID: result.SnapshotID, Sink: record.Sink, Status: result.Status, Error: result.Error,
	})
	return results
}
