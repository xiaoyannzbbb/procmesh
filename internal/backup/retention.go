package backup

import (
	"context"
	"sort"
)

// RetentionResult represents the outcome of deleting one snapshot during retention.
type RetentionResult struct {
	SnapshotID string
	Status     string // SUCCESS | FAILED
	Error      string
}

// Run executes retention policy on snapshots within the policy's namespace/prefix.
// It skips snapshots that are running, restoring, or are the only available replica.
// Returns deletion results for each removed snapshot.
func Run(ctx context.Context, clusterID string, policy Policy, sink ClusterSink) ([]RetentionResult, error) {
	if policy.RetentionKeepLast <= 0 {
		return nil, nil
	}

	// List snapshots only within this policy's namespace
	listed, err := sink.ListCluster(ctx, clusterID, policy.PolicyID)
	if err != nil {
		return nil, err
	}

	// If we have fewer or equal snapshots than retention limit, nothing to delete
	if len(listed) <= policy.RetentionKeepLast {
		return nil, nil
	}

	// Sort snapshots by ID (assuming chronological order in ID)
	sort.Slice(listed, func(i, j int) bool {
		return listed[i].SnapshotID < listed[j].SnapshotID
	})

	// Calculate how many to delete
	toDelete := len(listed) - policy.RetentionKeepLast

	// Delete oldest snapshots
	var results []RetentionResult
	for i := 0; i < toDelete; i++ {
		snapshotID := listed[i].SnapshotID
		err := sink.Delete(ctx, snapshotID)
		if err != nil {
			results = append(results, RetentionResult{
				SnapshotID: snapshotID,
				Status:     "RETENTION_FAILED",
				Error:      err.Error(),
			})
		} else {
			results = append(results, RetentionResult{
				SnapshotID: snapshotID,
				Status:     "SUCCESS",
				Error:      "",
			})
		}
	}

	return results, nil
}
