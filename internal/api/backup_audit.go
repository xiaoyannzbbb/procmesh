package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/qleelulu/procmesh/internal/store"
)

// controlMutation is the allowlisted audit payload for backup and replication writes.
// Only IDs, result, and a redacted error summary are persisted.
type controlMutation struct {
	Action, Resource, OperationID       string
	PolicyID, RunID, TaskID, SnapshotID string
	Result, Error                       string
}

func auditControlMutation(ctx context.Context, st *store.Store, localID string, rec controlMutation) {
	if st == nil {
		return
	}
	if rec.Result == "" {
		rec.Result = "SUCCESS"
	}
	if rec.Action == "backup.retention.delete" {
		result := "success"
		if rec.Result != "SUCCESS" {
			result = "error"
		}
		recordBackupRetentionDelete("fs", result)
	}
	meta := make(map[string]string, 5)
	if rec.PolicyID != "" {
		meta["policy_id"] = rec.PolicyID
	}
	if rec.RunID != "" {
		meta["run_id"] = rec.RunID
	}
	if rec.TaskID != "" {
		meta["task_id"] = rec.TaskID
	}
	if rec.SnapshotID != "" {
		meta["snapshot_id"] = rec.SnapshotID
	}
	if rec.Result != "SUCCESS" {
		meta["error"] = "failed"
	}
	raw, err := json.Marshal(meta)
	if err != nil || auditMetadataLeaked(raw) {
		raw = []byte(`{}`)
	}
	if rec.Resource == "" {
		switch {
		case rec.PolicyID != "":
			rec.Resource = "backup_policy:" + rec.PolicyID
		case rec.SnapshotID != "":
			rec.Resource = "backup:" + rec.SnapshotID
		case rec.RunID != "":
			rec.Resource = "backup_run:" + rec.RunID
		default:
			rec.Resource = rec.Action
		}
	}
	ev := store.AuditEvent{
		Resource:    rec.Resource,
		Action:      rec.Action,
		OperationID: rec.OperationID,
		Result:      rec.Result,
		SourceAgent: localID,
		Metadata:    raw,
	}
	if p, ok := PrincipalFrom(ctx); ok {
		ev.UserID = p.UserID
		ev.Username = p.Username
	}
	_ = st.AppendAudit(ctx, ev)
}

func auditMetadataLeaked(raw []byte) bool {
	s := strings.ToLower(string(raw))
	for _, leaked := range []string{"secret_key", "access_key", "payload", "s3://", "http://", "https://", "/var/lib/procmesh"} {
		if strings.Contains(s, leaked) {
			return true
		}
	}
	return false
}

func mutationResult(err error) controlMutation {
	if err == nil {
		return controlMutation{Result: "SUCCESS"}
	}
	return controlMutation{Result: "FAILED", Error: err.Error()}
}

func mergeMutation(rec, result controlMutation) controlMutation {
	rec.Result = result.Result
	rec.Error = result.Error
	return rec
}

var retentionTotals sync.Map // sink\x00result -> *atomic.Uint64

func recordBackupRetentionDelete(sink, result string) {
	switch sink {
	case "fs", "s3":
	default:
		sink = "fs"
	}
	if result != "success" && result != "error" {
		result = "error"
	}
	key := sink + "\x00" + result
	v, _ := retentionTotals.LoadOrStore(key, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}

func retentionDeleteTotals() map[string]map[string]uint64 {
	out := map[string]map[string]uint64{
		"fs": {"success": 0, "error": 0},
		"s3": {"success": 0, "error": 0},
	}
	retentionTotals.Range(func(key, val any) bool {
		k, ok := key.(string)
		if !ok {
			return true
		}
		n, ok := val.(*atomic.Uint64)
		if !ok || n == nil {
			return true
		}
		sink, result, found := strings.Cut(k, "\x00")
		if !found {
			return true
		}
		if out[sink] == nil {
			out[sink] = map[string]uint64{}
		}
		out[sink][result] = n.Load()
		return true
	})
	return out
}
