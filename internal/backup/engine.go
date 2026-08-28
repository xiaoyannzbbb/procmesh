package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

// Applier is the process write surface Restore uses. *process.Manager implements it.
type Applier interface {
	ApplySpec(ctx context.Context, spec process.ProcessSpec, expectedRevision int64, opID, operator, comment string) (process.ProcessSpec, error)
	GetSpec(ctx context.Context, processID string) (process.ProcessSpec, error)
}

// RestoreTarget is one process to restore, with CAS expected_revision.
type RestoreTarget struct {
	ProcessID        string
	ExpectedRevision int64
}

// RestoreResult is the per-process outcome of Restore.
type RestoreResult struct {
	ProcessID   string
	Status      string
	NewRevision int64
	Error       string
}

// PeerPusher sends a snapshot payload to a remote admitted node.
type PeerPusher interface {
	PutPeerSnapshot(ctx context.Context, nodeID string, sourceNodeID string, payload []byte) error
}

// PeerPushFunc adapts a function to PeerPusher.
type PeerPushFunc func(ctx context.Context, nodeID, sourceNodeID string, payload []byte) error

// PutPeerSnapshot calls f.
func (f PeerPushFunc) PutPeerSnapshot(ctx context.Context, nodeID, sourceNodeID string, payload []byte) error {
	return f(ctx, nodeID, sourceNodeID, payload)
}

// ReplicationPushRequest carries metadata for a source-owned peer transfer.
type ReplicationPushRequest struct {
	RunID, TaskID, PolicyID, TargetNodeID string
	PolicyRevision                        int64
	SnapshotID, SHA256                    string
}

type ReplicationPeerPusher interface {
	PutReplicationSnapshot(context.Context, ReplicationPushRequest, []byte) error
}

type ReplicationPeerPushFunc func(context.Context, ReplicationPushRequest, []byte) error

func (f ReplicationPeerPushFunc) PutReplicationSnapshot(ctx context.Context, req ReplicationPushRequest, payload []byte) error {
	return f(ctx, req, payload)
}

const ReplicaSinkName = "replica"

// CreateOpts selects processes, sink, and optional peer targets for Create.
type CreateOpts struct {
	ProcessIDs    []string
	Sink          string
	TargetNodeIDs []string
	SnapshotID    string // optional stable id for idempotent cluster task execution
}

// ClusterCreateOpts configures a cluster backup snapshot with namespaced paths.
type ClusterCreateOpts struct {
	RunID              string
	TaskID             string
	PolicyID           string
	ClusterID          string
	NodeID             string
	Sink               string
	DestinationProfile string
	ProcessIDs         []string
	SnapshotID         string
}

// ReplicationCaptureRequest captures a durable local replica snapshot for one run+source.
type ReplicationCaptureRequest struct {
	RunID, PolicyID, SourceNodeID, SnapshotID string
}

type DestinationHealth struct {
	Sink               string
	DestinationProfile string
	EndpointHost       string
	Status             string
	ErrorSummary       string
}

// Engine creates, reads, and deletes local process-spec snapshots.
type Engine struct {
	Store              *store.Store
	NodeID             string
	ClusterID          string
	Apply              Applier
	Sinks              map[string]Sink // "fs"|"s3"|"replica"
	ResolveDestination func(profile string) (Sink, error)
	PeerStore          *PeerStore
	PeerPush           PeerPusher
	ReplicationPush    ReplicationPeerPusher
	Admitted           func(nodeID string) bool
	DiskPercent        func() float64 // nil = 0
	Now                func() time.Time
	NewID              func() (string, error) // 测试注入；默认 UUID
	RetentionActive    func(snapshotID string) bool
	LastUsableReplica  func(snapshotID string) bool
	RetentionPolicy    func(policyID string) (Policy, bool)
	OnRetentionDelete  func(context.Context, RetentionDeleteEvent)
	LastSuccessUnix    atomic.Int64
	Schedule           string // 空 = 关；五字段 cron
	activeMu           sync.Mutex
	activeSnapshots    map[string]int
}

// ReplicateSnapshot reloads a stored primary payload by immutable ID and
// checksum. It never collects current process specs.
func (e *Engine) ReplicateSnapshot(ctx context.Context, req ReplicationTaskRequest) (int64, error) {
	if e == nil || e.Store == nil || e.ReplicationPush == nil {
		return 0, errcode.E(errcode.UNAVAILABLE, "replication source unavailable")
	}
	if req.SourceNodeID != e.NodeID || req.SnapshotID == "" || req.SHA256 == "" || req.TargetNodeID == "" {
		return 0, errcode.E(errcode.INVALID, "invalid replication source request")
	}
	release := e.ProtectSnapshot(req.SnapshotID)
	defer release()
	rec, err := e.Store.GetBackup(ctx, req.SnapshotID)
	if err != nil {
		return 0, err
	}
	clusterID := e.resolvedClusterID()
	if rec.NodeID != e.NodeID || rec.SHA256 != req.SHA256 || rec.ClusterID != clusterID {
		return 0, errcode.E(errcode.CONFLICT, "frozen snapshot checksum mismatch")
	}
	payload, err := e.readPayload(ctx, rec, rec.SnapshotID, rec.Sink)
	if err != nil {
		return 0, err
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != req.SHA256 {
		return 0, errcode.E(errcode.CONFLICT, "frozen snapshot checksum mismatch")
	}
	snapshot, err := Decode(payload)
	if err != nil || snapshot.SnapshotID != req.SnapshotID || snapshot.NodeID != e.NodeID || snapshot.ClusterID != clusterID {
		return 0, errcode.E(errcode.CONFLICT, "frozen snapshot payload mismatch")
	}
	if err := e.ReplicationPush.PutReplicationSnapshot(ctx, ReplicationPushRequest{RunID: req.RunID, TaskID: req.TaskID, PolicyID: req.PolicyID, PolicyRevision: req.PolicyRevision, TargetNodeID: req.TargetNodeID, SnapshotID: req.SnapshotID, SHA256: req.SHA256}, payload); err != nil {
		return 0, err
	}
	return int64(len(payload)), nil
}

var _ Applier = (*process.Manager)(nil)

func decodeRevisionRanges(raw string) []RevisionRange {
	var ranges []RevisionRange
	if raw == "" || json.Unmarshal([]byte(raw), &ranges) != nil {
		return []RevisionRange{}
	}
	return ranges
}

// Create snapshots local process specs + full revision history.
// ProcessIDs nil/empty means all local processes.
// sink fs/s3 ignores TargetNodeIDs; sink peer requires them.
func (e *Engine) Create(ctx context.Context, opt CreateOpts) (Meta, error) {
	if opt.Sink == "peer" {
		if err := e.validatePeerTargets(opt.TargetNodeIDs); err != nil {
			return Meta{}, err
		}
	} else if _, err := e.sink(opt.Sink); err != nil {
		return Meta{}, err
	}
	if opt.SnapshotID != "" && e.Store != nil {
		if rec, err := e.Store.GetBackup(ctx, opt.SnapshotID); err == nil {
			if rec.Sink == opt.Sink {
				return Meta{SnapshotID: rec.SnapshotID, ClusterID: rec.ClusterID, NodeID: rec.NodeID, CreatedAt: rec.CreatedAt, ProcessIDs: rec.ProcessIDs, RevisionRanges: decodeRevisionRanges(rec.RevisionRangesJSON), SHA256: rec.SHA256, Bytes: rec.Bytes, Sink: rec.Sink, Location: rec.Location, SourceNodeID: rec.SourceNodeID}, nil
			}
		} else if !errcode.Is(err, errcode.NOT_FOUND) {
			return Meta{}, err
		}
	}
	if e.diskPercent() >= 95 {
		return Meta{}, errcode.E(errcode.DEGRADED, "disk usage at or above 95%")
	}

	specs, err := e.collectSpecs(ctx, opt.ProcessIDs)
	if err != nil {
		return Meta{}, err
	}

	id := opt.SnapshotID
	if id == "" {
		id, err = e.newID()
		if err != nil {
			return Meta{}, err
		}
	}
	created := e.now()

	dumps := make([]ProcessDump, 0, len(specs))
	for _, spec := range specs {
		dump, err := e.dumpProcess(ctx, spec)
		if err != nil {
			return Meta{}, err
		}
		dumps = append(dumps, dump)
	}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    id,
		ClusterID:     e.ClusterID,
		NodeID:        e.NodeID,
		CreatedAt:     created,
		Processes:     dumps,
	}
	payload, sha, err := Encode(snap)
	if err != nil {
		return Meta{}, err
	}

	if opt.Sink == "peer" {
		return e.createPeer(ctx, snap, payload, sha, opt.TargetNodeIDs)
	}

	sink, err := e.sink(opt.Sink)
	if err != nil {
		return Meta{}, err
	}
	loc, err := sink.Put(ctx, id, payload)
	if err != nil {
		return Meta{}, err
	}
	return e.indexSnapshot(ctx, snap, sha, int64(len(payload)), opt.Sink, loc, "")
}

// CreateCluster snapshots local process specs with cluster namespace paths and idempotent task tracking.
func (e *Engine) CreateCluster(ctx context.Context, opts ClusterCreateOpts) (Meta, error) {
	if opts.RunID == "" || opts.TaskID == "" {
		return Meta{}, errcode.E(errcode.INVALID, "run_id and task_id required")
	}
	if opts.ClusterID == "" || opts.NodeID == "" {
		return Meta{}, errcode.E(errcode.INVALID, "cluster_id and node_id required")
	}
	if opts.Sink == "" {
		return Meta{}, errcode.E(errcode.INVALID, "sink required")
	}

	// Check idempotency: if task already completed with same checksum, return existing
	if e.Store != nil {
		if existing, err := e.Store.GetBackupByTask(ctx, opts.RunID, opts.TaskID); err == nil {
			// Task already executed, verify checksum matches
			specs, err := e.collectSpecs(ctx, opts.ProcessIDs)
			if err != nil {
				return Meta{}, err
			}
			dumps := make([]ProcessDump, 0, len(specs))
			for _, spec := range specs {
				dump, err := e.dumpProcess(ctx, spec)
				if err != nil {
					return Meta{}, err
				}
				dumps = append(dumps, dump)
			}
			snap := Snapshot{
				FormatVersion: 1,
				SnapshotID:    existing.SnapshotID,
				ClusterID:     opts.ClusterID,
				NodeID:        opts.NodeID,
				PolicyID:      existing.PolicyID,
				CreatedAt:     existing.CreatedAt,
				Processes:     dumps,
			}
			_, sha, err := Encode(snap)
			if err != nil {
				return Meta{}, err
			}
			if sha == existing.SHA256 {
				// Same task, same checksum - return existing
				return Meta{
					SnapshotID:     existing.SnapshotID,
					ClusterID:      existing.ClusterID,
					NodeID:         existing.NodeID,
					CreatedAt:      existing.CreatedAt,
					ProcessIDs:     existing.ProcessIDs,
					RevisionRanges: decodeRevisionRanges(existing.RevisionRangesJSON),
					SHA256:         existing.SHA256,
					Bytes:          existing.Bytes,
					Sink:           existing.Sink,
					Location:       existing.Location,
					SourceNodeID:   existing.SourceNodeID,
				}, nil
			}
			// Same task, different checksum - conflict
			return Meta{}, errcode.E(errcode.CONFLICT, "task checksum mismatch")
		} else if !errcode.Is(err, errcode.NOT_FOUND) {
			return Meta{}, err
		}
	}

	if e.diskPercent() >= 95 {
		return Meta{}, errcode.E(errcode.DEGRADED, "disk usage at or above 95%")
	}

	specs, err := e.collectSpecs(ctx, opts.ProcessIDs)
	if err != nil {
		return Meta{}, err
	}

	id := opts.SnapshotID
	if id == "" {
		var genErr error
		id, genErr = e.newID()
		if genErr != nil {
			return Meta{}, genErr
		}
	}
	created := e.now()

	dumps := make([]ProcessDump, 0, len(specs))
	for _, spec := range specs {
		dump, err := e.dumpProcess(ctx, spec)
		if err != nil {
			return Meta{}, err
		}
		dumps = append(dumps, dump)
	}

	snap := Snapshot{
		FormatVersion: 1,
		SnapshotID:    id,
		ClusterID:     opts.ClusterID,
		NodeID:        opts.NodeID,
		PolicyID:      opts.PolicyID,
		CreatedAt:     created,
		Processes:     dumps,
	}
	payload, sha, err := Encode(snap)
	if err != nil {
		return Meta{}, err
	}

	sink, err := e.clusterSink(opts.Sink, opts.DestinationProfile)
	if err != nil {
		return Meta{}, err
	}

	// Use cluster-aware sink if available
	var loc string
	if cs, ok := sink.(ClusterSink); ok {
		loc, err = cs.PutCluster(ctx, opts.ClusterID, opts.PolicyID, opts.NodeID, id, payload)
	} else {
		loc, err = sink.Put(ctx, id, payload)
	}
	if err != nil {
		return Meta{}, err
	}

	return e.indexClusterSnapshot(ctx, snap, sha, int64(len(payload)), opts.Sink, loc, opts.RunID, opts.TaskID, opts.PolicyID, opts.DestinationProfile)
}

// CaptureReplicationSnapshot writes a durable local replica snapshot for this
// source node. It does not create a ClusterBackupRun.
func (e *Engine) CaptureReplicationSnapshot(ctx context.Context, req ReplicationCaptureRequest) (Meta, error) {
	if req.SourceNodeID != e.NodeID {
		return Meta{}, errcode.E(errcode.INVALID, "capture source mismatch")
	}
	if req.SnapshotID == "" {
		req.SnapshotID = StableReplicationSnapshotID(req.RunID, req.SourceNodeID)
	}
	return e.CreateCluster(ctx, ClusterCreateOpts{
		RunID:      req.RunID,
		TaskID:     "capture:" + req.SourceNodeID,
		PolicyID:   req.PolicyID,
		ClusterID:  e.resolvedClusterID(),
		NodeID:     e.NodeID,
		Sink:       ReplicaSinkName,
		SnapshotID: req.SnapshotID,
	})
}

// CreatePeer is Create with Sink=peer.
func (e *Engine) CreatePeer(ctx context.Context, processIDs, targetNodeIDs []string) (Meta, error) {
	return e.Create(ctx, CreateOpts{
		ProcessIDs:    processIDs,
		Sink:          "peer",
		TargetNodeIDs: targetNodeIDs,
	})
}

func (e *Engine) validatePeerTargets(targets []string) error {
	if len(targets) == 0 {
		return errcode.E(errcode.INVALID, "target_node_ids required")
	}
	for _, id := range targets {
		if e.Admitted == nil || !e.Admitted(id) {
			return errcode.E(errcode.INVALID, "target node not admitted")
		}
	}
	return nil
}

func (e *Engine) createPeer(ctx context.Context, snap Snapshot, payload []byte, sha string, targets []string) (Meta, error) {
	if e.PeerStore == nil {
		return Meta{}, errcode.E(errcode.UNAVAILABLE, "peer store not configured")
	}
	var locs []string
	var pushErr error
	for _, node := range targets {
		if e.PeerPush == nil {
			pushErr = errcode.E(errcode.UNAVAILABLE, "peer push not configured")
			continue
		}
		if err := e.PeerPush.PutPeerSnapshot(ctx, node, e.NodeID, payload); err != nil {
			pushErr = errcode.E(errcode.UNAVAILABLE, "peer push failed")
			continue
		}
		locs = append(locs, "peer://"+node+"/"+snap.SnapshotID)
	}
	if len(locs) == 0 {
		if pushErr != nil {
			return Meta{}, pushErr
		}
		return Meta{}, errcode.E(errcode.UNAVAILABLE, "peer push failed")
	}
	// Keep a local peer copy so Restore/Get/Delete work on the Owner.
	// Receive never ApplySpec; restore still requires snap.NodeID == e.NodeID.
	if _, err := e.PeerStore.Receive(ctx, e.NodeID, payload); err != nil {
		return Meta{}, err
	}
	meta, err := e.indexSnapshot(ctx, snap, sha, int64(len(payload)), "peer", strings.Join(locs, ","), e.NodeID)
	if err != nil {
		return Meta{}, err
	}
	if pushErr != nil {
		return meta, pushErr
	}
	return meta, nil
}

func (e *Engine) indexSnapshot(ctx context.Context, snap Snapshot, sha string, bytes int64, sink, loc, sourceNodeID string) (Meta, error) {
	meta := MetaFromSnapshot(snap)
	meta.SHA256 = sha
	meta.Bytes = bytes
	meta.Sink = sink
	meta.Location = loc
	meta.SourceNodeID = sourceNodeID

	rangesJSON, err := json.Marshal(meta.RevisionRanges)
	if err != nil {
		return Meta{}, fmt.Errorf("marshal revision ranges: %w", err)
	}
	rec := store.BackupRecord{
		SnapshotID:         meta.SnapshotID,
		ClusterID:          meta.ClusterID,
		NodeID:             meta.NodeID,
		CreatedAt:          meta.CreatedAt,
		ProcessIDs:         meta.ProcessIDs,
		RevisionRangesJSON: string(rangesJSON),
		SHA256:             sha,
		Bytes:              bytes,
		Sink:               sink,
		Location:           loc,
		SourceNodeID:       sourceNodeID,
	}
	if err := e.Store.PutBackup(ctx, rec); err != nil {
		return Meta{}, err
	}
	e.LastSuccessUnix.Store(snap.CreatedAt.Unix())
	return meta, nil
}

func (e *Engine) indexClusterSnapshot(ctx context.Context, snap Snapshot, sha string, bytes int64, sink, loc, runID, taskID, policyID, destinationProfile string) (Meta, error) {
	meta := MetaFromSnapshot(snap)
	meta.SHA256 = sha
	meta.Bytes = bytes
	meta.Sink = sink
	meta.Location = loc

	rangesJSON, err := json.Marshal(meta.RevisionRanges)
	if err != nil {
		return Meta{}, fmt.Errorf("marshal revision ranges: %w", err)
	}
	rec := store.BackupRecord{
		SnapshotID:         meta.SnapshotID,
		ClusterID:          meta.ClusterID,
		NodeID:             meta.NodeID,
		CreatedAt:          meta.CreatedAt,
		ProcessIDs:         meta.ProcessIDs,
		RevisionRangesJSON: string(rangesJSON),
		SHA256:             sha,
		Bytes:              bytes,
		Sink:               sink,
		DestinationProfile: destinationProfile,
		Location:           loc,
		RunID:              runID,
		TaskID:             taskID,
		PolicyID:           policyID,
	}
	if err := e.Store.PutBackup(ctx, rec); err != nil {
		return Meta{}, err
	}
	e.LastSuccessUnix.Store(snap.CreatedAt.Unix())
	return meta, nil
}

// ReceivePeer stores a peer snapshot on disk and indexes it. It never Apply/ApplySpec.
func (e *Engine) ReceivePeer(ctx context.Context, sourceNodeID string, payload []byte) (Meta, error) {
	if e.PeerStore == nil {
		return Meta{}, errcode.E(errcode.UNAVAILABLE, "peer store not configured")
	}
	meta, err := e.PeerStore.Receive(ctx, sourceNodeID, payload)
	if err != nil {
		return Meta{}, err
	}
	rangesJSON, err := json.Marshal(meta.RevisionRanges)
	if err != nil {
		return Meta{}, fmt.Errorf("marshal revision ranges: %w", err)
	}
	rec := store.BackupRecord{
		SnapshotID:         meta.SnapshotID,
		ClusterID:          meta.ClusterID,
		NodeID:             meta.NodeID,
		CreatedAt:          meta.CreatedAt,
		ProcessIDs:         meta.ProcessIDs,
		RevisionRangesJSON: string(rangesJSON),
		SHA256:             meta.SHA256,
		Bytes:              meta.Bytes,
		Sink:               "peer",
		Location:           meta.Location,
		SourceNodeID:       sourceNodeID,
	}
	if err := e.Store.PutBackup(ctx, rec); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// Get returns index metadata and the sink payload. SHA-256 must match the index.
func (e *Engine) Get(ctx context.Context, snapshotID, sinkName string) (Meta, []byte, error) {
	if sinkName != "peer" {
		if _, err := e.sink(sinkName); err != nil {
			return Meta{}, nil, err
		}
	}
	rec, err := e.Store.GetBackup(ctx, snapshotID)
	if err != nil {
		return Meta{}, nil, err
	}
	payload, err := e.readPayload(ctx, rec, snapshotID, sinkName)
	if err != nil {
		return Meta{}, nil, err
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != rec.SHA256 {
		return Meta{}, nil, errcode.E(errcode.INVALID, "snapshot sha256 mismatch")
	}
	meta, err := metaFromRecord(rec)
	if err != nil {
		return Meta{}, nil, err
	}
	return meta, payload, nil
}

func (e *Engine) readPayload(ctx context.Context, rec store.BackupRecord, snapshotID, sinkName string) ([]byte, error) {
	if sinkName == "peer" {
		if e.PeerStore == nil {
			return nil, errcode.E(errcode.UNAVAILABLE, "peer store not configured")
		}
		if rec.ClusterID != "" {
			data, err := os.ReadFile(e.PeerStore.pathFor(rec.SourceNodeID, rec.ClusterID, snapshotID))
			if err == nil {
				return data, nil
			}
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("read cluster peer backup: %w", err)
			}
		}
		return e.PeerStore.Get(ctx, rec.SourceNodeID, snapshotID)
	}
	sink, err := e.clusterSink(sinkName, rec.DestinationProfile)
	if err != nil {
		sink, err = e.sink(sinkName)
	}
	if err != nil {
		return nil, err
	}
	if rec.ClusterID != "" && rec.PolicyID != "" && rec.NodeID != "" {
		if cs, ok := sink.(ClusterSink); ok {
			return cs.GetCluster(ctx, rec.ClusterID, rec.PolicyID, rec.NodeID, snapshotID)
		}
	}
	return sink.Get(ctx, snapshotID)
}

// Delete removes the sink object and the local index row.
func (e *Engine) Delete(ctx context.Context, snapshotID, sinkName string) error {
	if sinkName == "peer" {
		return e.deletePeer(ctx, snapshotID)
	}
	sink, err := e.sink(sinkName)
	if err != nil {
		return err
	}
	if err := sink.Delete(ctx, snapshotID); err != nil {
		return err
	}
	return e.Store.DeleteBackup(ctx, snapshotID)
}

func (e *Engine) deletePeer(ctx context.Context, snapshotID string) error {
	rec, err := e.Store.GetBackup(ctx, snapshotID)
	if err != nil {
		return err
	}
	if e.PeerStore != nil && rec.SourceNodeID != "" {
		if err := e.PeerStore.Delete(ctx, rec.SourceNodeID, snapshotID); err != nil && !errcode.Is(err, errcode.NOT_FOUND) {
			return err
		}
	}
	return e.Store.DeleteBackup(ctx, snapshotID)
}

// ListLocal returns backup_index rows as Meta.
func (e *Engine) ListLocal(ctx context.Context) ([]Meta, error) {
	recs, err := e.Store.ListBackups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(recs))
	for _, rec := range recs {
		m, err := metaFromRecord(rec)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Restore applies snapshot specs via ApplySpec + CAS. Partial CONFLICT is
// returned in results with a nil error. Does not call store.PutSpec.
func (e *Engine) Restore(ctx context.Context, snapshotID, sinkName, opID, operator string, targets []RestoreTarget) ([]RestoreResult, error) {
	if len(targets) == 0 {
		return nil, errcode.E(errcode.INVALID, "targets required")
	}
	release := e.ProtectSnapshot(snapshotID)
	defer release()
	_, payload, err := e.Get(ctx, snapshotID, sinkName)
	if err != nil {
		return nil, err
	}
	snap, err := Decode(payload)
	if err != nil {
		return nil, err
	}
	results := make([]RestoreResult, 0, len(targets))
	if snap.NodeID != e.NodeID {
		for _, t := range targets {
			results = append(results, RestoreResult{
				ProcessID: t.ProcessID,
				Status:    "INVALID",
				Error:     "cannot restore another node's process on this agent",
			})
		}
		return results, nil
	}
	byID := make(map[string]ProcessDump, len(snap.Processes))
	for _, p := range snap.Processes {
		byID[p.ProcessID] = p
	}
	comment := "restore from snapshot " + snapshotID
	for _, t := range targets {
		results = append(results, e.restoreOne(ctx, t, byID, opID, operator, comment))
	}
	return results, nil
}

// ProtectSnapshot holds a snapshot against retention until the returned
// release function is called. Restore uses it automatically; replication
// callers use the same lifecycle while copying payloads.
func (e *Engine) ProtectSnapshot(snapshotID string) func() {
	if e == nil || snapshotID == "" {
		return func() {}
	}
	e.activeMu.Lock()
	if e.activeSnapshots == nil {
		e.activeSnapshots = make(map[string]int)
	}
	e.activeSnapshots[snapshotID]++
	e.activeMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			e.activeMu.Lock()
			defer e.activeMu.Unlock()
			if e.activeSnapshots[snapshotID] <= 1 {
				delete(e.activeSnapshots, snapshotID)
				return
			}
			e.activeSnapshots[snapshotID]--
		})
	}
}

func (e *Engine) retentionActive(snapshotID string) bool {
	if e.RetentionActive != nil && e.RetentionActive(snapshotID) {
		return true
	}
	e.activeMu.Lock()
	defer e.activeMu.Unlock()
	return e.activeSnapshots[snapshotID] > 0
}

func (e *Engine) restoreOne(ctx context.Context, t RestoreTarget, byID map[string]ProcessDump, opID, operator, comment string) RestoreResult {
	r := RestoreResult{ProcessID: t.ProcessID}
	if t.ProcessID == "" {
		r.Status = "INVALID"
		r.Error = "process_id required"
		return r
	}
	dump, ok := byID[t.ProcessID]
	if !ok {
		r.Status = "INVALID"
		r.Error = "process not in snapshot"
		return r
	}
	raw, err := LatestSpec(dump)
	if err != nil {
		return restoreFail(r, err)
	}
	var spec process.ProcessSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		r.Status = "INVALID"
		r.Error = err.Error()
		return r
	}
	spec.ProcessID = dump.ProcessID
	if dump.Name != "" {
		spec.Name = dump.Name
	}
	if e.Apply == nil {
		r.Status = "UNAVAILABLE"
		r.Error = "apply not configured"
		return r
	}
	if _, err := e.Apply.GetSpec(ctx, t.ProcessID); errcode.Is(err, errcode.NOT_FOUND) {
		if t.ExpectedRevision != 0 {
			r.Status = "CONFLICT"
			r.Error = "process not found"
			return r
		}
	} else if err != nil {
		return restoreFail(r, err)
	}
	out, err := e.Apply.ApplySpec(ctx, spec, t.ExpectedRevision, opID+":"+t.ProcessID, operator, comment)
	if err != nil {
		return restoreApplyFail(r, err, t.ExpectedRevision)
	}
	r.Status = "SUCCESS"
	r.NewRevision = out.LatestRevision
	return r
}

func restoreApplyFail(r RestoreResult, err error, expected int64) RestoreResult {
	if errcode.Is(err, errcode.NOT_FOUND) {
		if expected != 0 {
			r.Status = "CONFLICT"
			r.Error = err.Error()
			return r
		}
		r.Status = "INVALID"
		r.Error = err.Error()
		return r
	}
	return restoreFail(r, err)
}

func restoreFail(r RestoreResult, err error) RestoreResult {
	r.Error = err.Error()
	switch {
	case errcode.Is(err, errcode.CONFLICT):
		r.Status = "CONFLICT"
	case errcode.Is(err, errcode.UNAVAILABLE), errcode.Is(err, errcode.TIMEOUT), errcode.Is(err, errcode.DEGRADED):
		r.Status = "UNAVAILABLE"
	default:
		r.Status = "INVALID"
	}
	return r
}

func (e *Engine) sink(name string) (Sink, error) {
	if e.Sinks != nil {
		if s, ok := e.Sinks[name]; ok && s != nil {
			return s, nil
		}
	}
	return nil, errcode.E(errcode.INVALID, "unknown sink")
}

type destinationProfileError struct {
	profile string
	err     error
}

func (e *destinationProfileError) Error() string {
	return "destination profile not configured"
}

func (e *destinationProfileError) Unwrap() error { return e.err }

func (e *Engine) clusterSink(name, profile string) (Sink, error) {
	if name == ReplicaSinkName {
		return e.sink(name)
	}
	if name != "s3" || e.ResolveDestination == nil {
		return e.sink(name)
	}
	if profile == "" {
		return nil, &destinationProfileError{profile: profile, err: errcode.E(errcode.INVALID, "destination profile required")}
	}
	sink, err := e.ResolveDestination(profile)
	if err != nil || sink == nil {
		if err == nil {
			err = errcode.E(errcode.INVALID, "destination profile not configured")
		}
		return nil, &destinationProfileError{profile: profile, err: err}
	}
	return sink, nil
}

type destinationChecker interface {
	CheckDestination(context.Context) error
	EndpointHost() string
}

func (e *Engine) CheckDestination(ctx context.Context, sinkName, profile string) DestinationHealth {
	health := DestinationHealth{Sink: sinkName, DestinationProfile: profile}
	sink, err := e.clusterSink(sinkName, profile)
	if err != nil {
		var profileErr *destinationProfileError
		if errors.As(err, &profileErr) {
			health.Status = "CONFIG_MISSING"
			health.ErrorSummary = profileErr.Error()
			return health
		}
		health.Status = "CONFIG_MISSING"
		health.ErrorSummary = err.Error()
		return health
	}
	checker, ok := sink.(destinationChecker)
	if !ok {
		health.Status = "AVAILABLE"
		return health
	}
	health.EndpointHost = checker.EndpointHost()
	if err := checker.CheckDestination(ctx); err != nil {
		health.Status = "UNAVAILABLE"
		health.ErrorSummary = err.Error()
		return health
	}
	health.Status = "AVAILABLE"
	return health
}

func (e *Engine) diskPercent() float64 {
	if e.DiskPercent == nil {
		return 0
	}
	return e.DiskPercent()
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func (e *Engine) newID() (string, error) {
	if e.NewID != nil {
		return e.NewID()
	}
	return newUUID()
}

func (e *Engine) collectSpecs(ctx context.Context, processIDs []string) ([]process.ProcessSpec, error) {
	if len(processIDs) == 0 {
		specs, err := e.Store.ListSpecs(ctx)
		if err != nil {
			return nil, err
		}
		if len(specs) == 0 {
			return nil, errcode.E(errcode.INVALID, "no processes to backup")
		}
		return specs, nil
	}
	out := make([]process.ProcessSpec, 0, len(processIDs))
	for _, id := range processIDs {
		spec, err := e.Store.GetSpec(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}

func (e *Engine) dumpProcess(ctx context.Context, spec process.ProcessSpec) (ProcessDump, error) {
	revs, err := e.Store.ListRevisionDumps(ctx, spec.ProcessID)
	if err != nil {
		return ProcessDump{}, err
	}
	dump := ProcessDump{
		ProcessID: spec.ProcessID,
		Name:      spec.Name,
		Revisions: make([]RevisionDump, 0, len(revs)),
	}
	for i, r := range revs {
		dump.Revisions = append(dump.Revisions, RevisionDump{
			Revision:  r.Revision,
			Operator:  r.Operator,
			Timestamp: r.Timestamp,
			Diff:      r.Diff,
			Comment:   r.Comment,
			Spec:      json.RawMessage(r.SpecJSON),
		})
		if i == 0 || r.Revision < dump.MinRevision {
			dump.MinRevision = r.Revision
		}
		if r.Revision > dump.MaxRevision {
			dump.MaxRevision = r.Revision
		}
	}
	return dump, nil
}

func metaFromRecord(rec store.BackupRecord) (Meta, error) {
	m := Meta{
		SnapshotID:   rec.SnapshotID,
		ClusterID:    rec.ClusterID,
		NodeID:       rec.NodeID,
		CreatedAt:    rec.CreatedAt,
		ProcessIDs:   rec.ProcessIDs,
		SHA256:       rec.SHA256,
		Bytes:        rec.Bytes,
		Sink:         rec.Sink,
		Location:     rec.Location,
		SourceNodeID: rec.SourceNodeID,
	}
	if m.ProcessIDs == nil {
		m.ProcessIDs = []string{}
	}
	if rec.RevisionRangesJSON == "" || rec.RevisionRangesJSON == "null" {
		m.RevisionRanges = []RevisionRange{}
		return m, nil
	}
	if err := json.Unmarshal([]byte(rec.RevisionRangesJSON), &m.RevisionRanges); err != nil {
		return Meta{}, fmt.Errorf("parse revision ranges: %w", err)
	}
	if m.RevisionRanges == nil {
		m.RevisionRanges = []RevisionRange{}
	}
	return m, nil
}

func (e *Engine) resolvedClusterID() string {
	if e == nil {
		return ""
	}
	if e.ClusterID != "" {
		return e.ClusterID
	}
	if e.Store != nil {
		id, err := e.Store.GetClusterID(context.Background())
		if err == nil && id != "" {
			e.ClusterID = id
			return id
		}
	}
	return ""
}

// StableReplicationSnapshotID is the idempotent replica snapshot name for one
// run and source node. Every route of that run shares this object.
func StableReplicationSnapshotID(runID, sourceNodeID string) string {
	sum := sha256.Sum256([]byte("replica-snap\x00" + runID + "\x00" + sourceNodeID))
	return hex.EncodeToString(sum[:16])
}

// StableClusterSnapshotID is the idempotent object name for a cluster task.
func StableClusterSnapshotID(runID, taskID string) string {
	sum := sha256.Sum256([]byte(runID + ":" + taskID))
	return "snap-" + hex.EncodeToString(sum[:12])
}

func (e *Engine) stableTaskSnapshotID(runID, taskID string) string {
	if e != nil && e.NewID != nil {
		return ""
	}
	return StableClusterSnapshotID(runID, taskID)
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// RunClusterTask executes a cluster backup task locally and returns the result.
// Implements idempotency: if the same run_id+task_id is executed again, returns the existing result.
func (e *Engine) RunClusterTask(ctx context.Context, req ClusterTaskRequest) (*TaskResult, error) {
	if req.RunID == "" || req.TaskID == "" {
		return nil, errcode.E(errcode.INVALID, "run_id and task_id required")
	}
	if req.NodeID == "" {
		return nil, errcode.E(errcode.INVALID, "node_id required")
	}
	clusterID := e.resolvedClusterID()
	if clusterID == "" || e.NodeID == "" {
		return nil, errcode.E(errcode.INVALID, "cluster_id and node_id not configured")
	}

	// Execute the cluster backup
	meta, err := e.CreateCluster(ctx, ClusterCreateOpts{
		RunID:              req.RunID,
		TaskID:             req.TaskID,
		PolicyID:           req.PolicyID,
		ClusterID:          clusterID,
		NodeID:             e.NodeID,
		Sink:               req.Sink,
		DestinationProfile: req.DestinationProfile,
		ProcessIDs:         req.ProcessIDs,
		SnapshotID:         e.stableTaskSnapshotID(req.RunID, req.TaskID),
	})

	result := &TaskResult{
		RunID:       req.RunID,
		TaskID:      req.TaskID,
		NodeID:      e.NodeID,
		LeaderTerm:  req.LeaderTerm,
		UpdatedUnix: e.now().Unix(),
	}

	if err != nil {
		var profileErr *destinationProfileError
		if errors.As(err, &profileErr) {
			result.Status = "CONFIG_MISSING"
			result.ErrorCode = "CONFIG_MISSING"
			result.ErrorSummary = profileErr.Error()
			return result, nil
		}
		result.Status = "FAILED"
		// Extract error code
		var e *errcode.Error
		if errors.As(err, &e) {
			result.ErrorCode = string(e.Code)
		} else {
			result.ErrorCode = "UNKNOWN"
		}
		result.ErrorSummary = err.Error()
		return result, nil
	}

	result.SnapshotID = meta.SnapshotID
	result.SHA256 = meta.SHA256
	result.Bytes = meta.Bytes
	result.Status = "SUCCESS"
	if e.RetentionPolicy != nil {
		if policy, ok := e.RetentionPolicy(req.PolicyID); ok {
			policy.Sink = req.Sink
			policy.DestinationProfile = req.DestinationProfile
			retentionResults, retentionErr := e.ApplyRetention(ctx, policy)
			if retentionErr != nil {
				result.Status = "RETENTION_FAILED"
				result.ErrorCode = "RETENTION_EXECUTION_FAILED"
				result.ErrorSummary = retentionErr.Error()
				return result, nil
			}
			for _, retentionResult := range retentionResults {
				if retentionResult.Status == "RETENTION_FAILED" {
					result.Status = "RETENTION_FAILED"
					result.ErrorCode = retentionResult.ErrorCode
					result.ErrorSummary = retentionResult.Error
					return result, nil
				}
			}
		}
	}
	return result, nil
}

// GetClusterTask retrieves the status of a previously executed cluster backup task.
func (e *Engine) GetClusterTask(ctx context.Context, runID, taskID string) (*TaskResult, error) {
	if runID == "" || taskID == "" {
		return nil, errcode.E(errcode.INVALID, "run_id and task_id required")
	}
	if e.Store == nil {
		return nil, errcode.E(errcode.UNAVAILABLE, "store not configured")
	}

	// Retrieve the backup record by task
	rec, err := e.Store.GetBackupByTask(ctx, runID, taskID)
	if err != nil {
		if errcode.Is(err, errcode.NOT_FOUND) {
			return &TaskResult{
				RunID:       runID,
				TaskID:      taskID,
				Status:      "NOT_FOUND",
				UpdatedUnix: e.now().Unix(),
			}, nil
		}
		return nil, err
	}

	result := &TaskResult{
		RunID:       runID,
		TaskID:      taskID,
		NodeID:      rec.NodeID,
		SnapshotID:  rec.SnapshotID,
		SHA256:      rec.SHA256,
		Status:      "SUCCESS",
		UpdatedUnix: rec.CreatedAt.Unix(),
	}

	return result, nil
}
