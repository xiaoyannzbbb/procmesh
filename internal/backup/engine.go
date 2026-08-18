package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/store"
)

// Engine creates, reads, and deletes local process-spec snapshots.
type Engine struct {
	Store           *store.Store
	NodeID          string
	ClusterID       string
	Sinks           map[string]Sink // "fs"|"s3"
	DiskPercent     func() float64  // nil = 0
	Now             func() time.Time
	NewID           func() (string, error) // 测试注入；默认 UUID
	LastSuccessUnix atomic.Int64
}

// Create snapshots local process specs + full revision history to sinkName.
// processIDs nil/empty means all local processes.
func (e *Engine) Create(ctx context.Context, processIDs []string, sinkName string) (Meta, error) {
	sink, err := e.sink(sinkName)
	if err != nil {
		return Meta{}, err
	}
	if e.diskPercent() >= 95 {
		return Meta{}, errcode.E(errcode.DEGRADED, "disk usage at or above 95%")
	}

	specs, err := e.collectSpecs(ctx, processIDs)
	if err != nil {
		return Meta{}, err
	}

	id, err := e.newID()
	if err != nil {
		return Meta{}, err
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
	loc, err := sink.Put(ctx, id, payload)
	if err != nil {
		return Meta{}, err
	}

	meta := MetaFromSnapshot(snap)
	meta.SHA256 = sha
	meta.Sink = sinkName
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
		Sink:               sinkName,
		Location:           loc,
	}
	if err := e.Store.PutBackup(ctx, rec); err != nil {
		return Meta{}, err
	}
	e.LastSuccessUnix.Store(created.Unix())
	return meta, nil
}

// Get returns index metadata and the sink payload. SHA-256 must match the index.
func (e *Engine) Get(ctx context.Context, snapshotID, sinkName string) (Meta, []byte, error) {
	sink, err := e.sink(sinkName)
	if err != nil {
		return Meta{}, nil, err
	}
	rec, err := e.Store.GetBackup(ctx, snapshotID)
	if err != nil {
		return Meta{}, nil, err
	}
	payload, err := sink.Get(ctx, snapshotID)
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

// Delete removes the sink object and the local index row.
func (e *Engine) Delete(ctx context.Context, snapshotID, sinkName string) error {
	sink, err := e.sink(sinkName)
	if err != nil {
		return err
	}
	if err := sink.Delete(ctx, snapshotID); err != nil {
		return err
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

func (e *Engine) sink(name string) (Sink, error) {
	if e.Sinks != nil {
		if s, ok := e.Sinks[name]; ok && s != nil {
			return s, nil
		}
	}
	return nil, errcode.E(errcode.INVALID, "unknown sink")
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

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
