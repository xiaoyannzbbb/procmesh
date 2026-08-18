package api

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/freshness"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.BackupServiceHandler = (*BackupAPI)(nil)

type BackupAPI struct {
	Engine    *backup.Engine
	Auth      *auth.Service
	LocalOnly bool
	LocalID   string
	Router    *Router
	Forward   Forwarder
	Now       func() time.Time
}

func (s *BackupAPI) CreateBackup(ctx context.Context, req *connect.Request[procmeshv1.CreateBackupRequest]) (*connect.Response[procmeshv1.CreateBackupResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermBackupManage, s.LocalID, true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	sink := req.Msg.GetSink()
	if sink == "" {
		sink = "fs"
	}
	meta, err := s.Engine.Create(ctx, backup.CreateOpts{
		ProcessIDs:    req.Msg.GetProcessIds(),
		Sink:          sink,
		TargetNodeIDs: req.Msg.GetTargetNodeIds(),
	})
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.CreateBackupResponse{Snapshot: metaToProto(meta)}), nil
}

func (s *BackupAPI) ListBackups(ctx context.Context, req *connect.Request[procmeshv1.ListBackupsRequest]) (*connect.Response[procmeshv1.ListBackupsResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermBackupRead, s.LocalID, false, true); err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	limit := normalizeAuditLimit(req.Msg.GetLimit())
	now := s.now()
	entries, err := s.listLocal(ctx, req.Msg.GetSink(), now)
	if err != nil {
		return nil, ToConnect(err)
	}
	if req.Msg.GetIncludeS3() {
		entries = append(entries, s.listS3(ctx, now)...)
	}
	if !s.LocalOnly {
		for _, peer := range req.Msg.GetPeerNodeIds() {
			if peer == "" || peer == s.LocalID {
				continue
			}
			entries = append(entries, s.listPeer(ctx, req, peer, now)...)
		}
	}
	entries = applyBackupLimit(entries, limit)
	return connect.NewResponse(&procmeshv1.ListBackupsResponse{Entries: entries}), nil
}

func (s *BackupAPI) GetBackup(ctx context.Context, req *connect.Request[procmeshv1.GetBackupRequest]) (*connect.Response[procmeshv1.GetBackupResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermBackupRead, s.LocalID, false, true); err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	if local, rt, hop := s.maybeHop(ctx, req.Header(), req.Msg.GetSourceNodeId()); hop {
		if !local {
			cli, err := s.remoteBackup(ctx, rt, req.Header())
			if err != nil {
				return nil, err
			}
			hopCtx, cancel := context.WithTimeout(ctx, rpc.UnaryTimeout)
			defer cancel()
			out, err := cli.GetBackup(hopCtx, req)
			if err != nil {
				return nil, mapForwardErr(err)
			}
			return out, nil
		}
	}
	sink := req.Msg.GetSink()
	if sink == "" {
		sink = "fs"
	}
	meta, payload, err := s.Engine.Get(ctx, req.Msg.GetSnapshotId(), sink)
	if err != nil {
		return nil, ToConnect(err)
	}
	out := &procmeshv1.GetBackupResponse{Snapshot: metaToProto(meta)}
	if req.Msg.GetIncludePayload() {
		out.Payload = payload
	}
	return connect.NewResponse(out), nil
}

func (s *BackupAPI) DeleteBackup(ctx context.Context, req *connect.Request[procmeshv1.DeleteBackupRequest]) (*connect.Response[procmeshv1.DeleteBackupResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermBackupManage, s.LocalID, true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	if local, rt, hop := s.maybeHop(ctx, req.Header(), req.Msg.GetSourceNodeId()); hop {
		if !local {
			cli, err := s.remoteBackup(ctx, rt, req.Header())
			if err != nil {
				return nil, err
			}
			hopCtx, cancel := context.WithTimeout(ctx, rpc.MutationTimeout)
			defer cancel()
			out, err := cli.DeleteBackup(hopCtx, req)
			if err != nil {
				return nil, mapForwardErr(err)
			}
			return out, nil
		}
	}
	sink := req.Msg.GetSink()
	if sink == "" {
		sink = "fs"
	}
	if err := s.Engine.Delete(ctx, req.Msg.GetSnapshotId(), sink); err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.DeleteBackupResponse{}), nil
}

func (s *BackupAPI) RestoreBackup(ctx context.Context, req *connect.Request[procmeshv1.RestoreBackupRequest]) (*connect.Response[procmeshv1.RestoreBackupResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermBackupManage, s.LocalID, true, true); err != nil {
		return nil, err
	}
	opID, operator, err := metaOf(req.Msg.GetMeta())
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetTargets()) == 0 {
		return nil, ToConnect(errcode.E(errcode.INVALID, "targets required"))
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	operator = operatorOf(ctx, operator)
	ownerID, ok := s.lookupSnapshotOwner(ctx, req.Msg.GetSnapshotId(), req.Msg.GetSink())
	if ok && isBackupNodeID(ownerID) && ownerID != s.LocalID {
		if s.LocalOnly {
			return nil, ToConnect(errcode.E(errcode.INVALID, "cannot restore another node's process on this agent"))
		}
		return s.hopRestore(ctx, req, ownerID)
	}
	if src := req.Msg.GetSourceNodeId(); !ok && isBackupNodeID(src) && src != s.LocalID && !s.LocalOnly {
		return s.hopRestore(ctx, req, src)
	}
	sink := req.Msg.GetSink()
	if sink == "" {
		sink = "fs"
	}
	results, err := s.Engine.Restore(ctx, req.Msg.GetSnapshotId(), sink, opID, operator, protoRestoreTargets(req.Msg.GetTargets()))
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.RestoreBackupResponse{Results: protoRestoreResults(results)}), nil
}

func (s *BackupAPI) PutPeerSnapshot(ctx context.Context, req *connect.Request[procmeshv1.PutPeerSnapshotRequest]) (*connect.Response[procmeshv1.PutPeerSnapshotResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermBackupManage, s.LocalID, true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := s.requireEngine(); err != nil {
		return nil, err
	}
	meta, err := s.Engine.ReceivePeer(ctx, req.Msg.GetSourceNodeId(), req.Msg.GetPayload())
	if err != nil {
		return nil, ToConnect(err)
	}
	return connect.NewResponse(&procmeshv1.PutPeerSnapshotResponse{Snapshot: metaToProto(meta)}), nil
}

func (s *BackupAPI) listLocal(ctx context.Context, sink string, now time.Time) ([]*procmeshv1.BackupEntry, error) {
	metas, err := s.Engine.ListLocal(ctx)
	if err != nil {
		return nil, err
	}
	nowMs := now.UnixMilli()
	out := make([]*procmeshv1.BackupEntry, 0, len(metas))
	for _, m := range metas {
		if sink != "" && m.Sink != sink {
			continue
		}
		out = append(out, &procmeshv1.BackupEntry{
			Snapshot:          metaToProto(m),
			SourceNode:        s.LocalID,
			Freshness:         freshness.LIVE,
			LastUpdatedUnixMs: nowMs,
		})
	}
	return out, nil
}

func (s *BackupAPI) listS3(ctx context.Context, now time.Time) []*procmeshv1.BackupEntry {
	sink := s.s3Sink()
	if sink == nil {
		return nil
	}
	listed, err := sink.List(ctx)
	if err != nil {
		return []*procmeshv1.BackupEntry{s.stalePlaceholder("s3", now)}
	}
	seen := map[string]struct{}{}
	if local, lerr := s.Engine.ListLocal(ctx); lerr == nil {
		for _, m := range local {
			if m.Sink == "s3" {
				seen[m.SnapshotID] = struct{}{}
			}
		}
	}
	out := make([]*procmeshv1.BackupEntry, 0)
	nowMs := now.UnixMilli()
	for _, item := range listed {
		if _, ok := seen[item.SnapshotID]; ok {
			continue
		}
		payload, gerr := sink.Get(ctx, item.SnapshotID)
		if gerr != nil {
			continue
		}
		snap, derr := backup.Decode(payload)
		if derr != nil {
			continue
		}
		meta := backup.MetaFromSnapshot(snap)
		meta.Sink = "s3"
		meta.Location = item.Location
		out = append(out, &procmeshv1.BackupEntry{
			Snapshot:          metaToProto(meta),
			SourceNode:        "s3",
			Freshness:         freshness.LIVE,
			LastUpdatedUnixMs: nowMs,
		})
	}
	return out
}

func (s *BackupAPI) listPeer(ctx context.Context, req *connect.Request[procmeshv1.ListBackupsRequest], peerID string, now time.Time) []*procmeshv1.BackupEntry {
	rt, err := s.routeToNode(ctx, req.Header(), peerID)
	if err != nil || rt.Local {
		return []*procmeshv1.BackupEntry{s.stalePlaceholder(peerID, now)}
	}
	hopCtx, cancel := context.WithTimeout(ctx, rpc.UnaryTimeout)
	defer cancel()
	cli, err := s.remoteBackup(hopCtx, rt, req.Header())
	if err != nil {
		return []*procmeshv1.BackupEntry{s.stalePlaceholder(peerID, now)}
	}
	fwd := connect.NewRequest(&procmeshv1.ListBackupsRequest{
		Sink:  req.Msg.GetSink(),
		Limit: req.Msg.GetLimit(),
	})
	for k, vs := range req.Header() {
		for _, v := range vs {
			fwd.Header().Add(k, v)
		}
	}
	stampHop(fwd.Header(), s.LocalID, rt.NodeID)
	stampIdentity(fwd.Header(), ctx)
	out, err := cli.ListBackups(hopCtx, fwd)
	if err != nil || out == nil || out.Msg == nil {
		return []*procmeshv1.BackupEntry{s.stalePlaceholder(peerID, now)}
	}
	ents := out.Msg.GetEntries()
	if len(ents) == 0 {
		return nil
	}
	return ents
}

func (s *BackupAPI) hopRestore(ctx context.Context, req *connect.Request[procmeshv1.RestoreBackupRequest], nodeID string) (*connect.Response[procmeshv1.RestoreBackupResponse], error) {
	rt, err := s.routeToNode(ctx, req.Header(), nodeID)
	if err != nil {
		return nil, ToConnect(err)
	}
	if rt.Local {
		return nil, ToConnect(errcode.E(errcode.INVALID, "cannot restore another node's process on this agent"))
	}
	cli, err := s.remoteBackup(ctx, rt, req.Header())
	if err != nil {
		return nil, err
	}
	hopCtx, cancel := context.WithTimeout(ctx, rpc.MutationTimeout)
	defer cancel()
	out, err := cli.RestoreBackup(hopCtx, req)
	if err != nil {
		return nil, mapForwardErr(err)
	}
	return out, nil
}

func (s *BackupAPI) lookupSnapshotOwner(ctx context.Context, snapshotID, sink string) (string, bool) {
	if sink == "" {
		sink = "fs"
	}
	if meta, _, err := s.Engine.Get(ctx, snapshotID, sink); err == nil && isBackupNodeID(meta.NodeID) {
		return meta.NodeID, true
	}
	metas, err := s.Engine.ListLocal(ctx)
	if err != nil {
		return "", false
	}
	for _, m := range metas {
		if m.SnapshotID == snapshotID && isBackupNodeID(m.NodeID) {
			return m.NodeID, true
		}
	}
	return "", false
}

func isBackupNodeID(id string) bool {
	return id != "" && id != "s3"
}

func (s *BackupAPI) maybeHop(ctx context.Context, header http.Header, sourceNodeID string) (local bool, rt Route, hop bool) {
	if sourceNodeID == "" || sourceNodeID == s.LocalID || s.LocalOnly {
		return true, Route{Local: true, NodeID: s.LocalID}, false
	}
	rt, err := s.routeToNode(ctx, header, sourceNodeID)
	if err != nil {
		return false, Route{}, true
	}
	return rt.Local, rt, true
}

func (s *BackupAPI) routeToNode(ctx context.Context, header http.Header, nodeID string) (Route, error) {
	if nodeID == "" || nodeID == s.LocalID {
		return Route{Local: true, NodeID: s.LocalID}, nil
	}
	h := header.Clone()
	rpc.SetTarget(h, nodeID)
	local, rt, err := hopRoute(s.LocalOnly, s.LocalID, s.Router, ctx, h, "", nodeID)
	if err != nil {
		return Route{}, err
	}
	if local {
		return Route{Local: true, NodeID: s.LocalID}, nil
	}
	return rt, nil
}

func (s *BackupAPI) remoteBackup(ctx context.Context, rt Route, header http.Header) (procmeshv1connect.BackupServiceClient, error) {
	if s.Forward == nil {
		return nil, unavailableOwner()
	}
	stampHop(header, s.LocalID, rt.NodeID)
	stampIdentity(header, ctx)
	cli, err := s.Forward.Backup(ctx, rt)
	if err != nil {
		return nil, ToConnect(rpc.MapDialError(err))
	}
	return cli, nil
}

func (s *BackupAPI) s3Sink() backup.Sink {
	if s.Engine == nil || s.Engine.Sinks == nil {
		return nil
	}
	return s.Engine.Sinks["s3"]
}

func (s *BackupAPI) stalePlaceholder(source string, now time.Time) *procmeshv1.BackupEntry {
	return &procmeshv1.BackupEntry{
		Snapshot:          nil,
		SourceNode:        source,
		Freshness:         freshness.STALE,
		LastUpdatedUnixMs: now.UnixMilli(),
	}
}

func (s *BackupAPI) requireEngine() error {
	if s == nil || s.Engine == nil {
		return ToConnect(errcode.E(errcode.UNAVAILABLE, "backup not configured"))
	}
	return nil
}

func (s *BackupAPI) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func metaToProto(m backup.Meta) *procmeshv1.BackupSnapshot {
	ranges := make([]*procmeshv1.BackupRevisionRange, 0, len(m.RevisionRanges))
	for _, r := range m.RevisionRanges {
		ranges = append(ranges, &procmeshv1.BackupRevisionRange{
			ProcessId:   r.ProcessID,
			MinRevision: r.MinRevision,
			MaxRevision: r.MaxRevision,
		})
	}
	var created int64
	if !m.CreatedAt.IsZero() {
		created = m.CreatedAt.UTC().UnixMilli()
	}
	return &procmeshv1.BackupSnapshot{
		SnapshotId:     m.SnapshotID,
		ClusterId:      m.ClusterID,
		NodeId:         m.NodeID,
		CreatedUnixMs:  created,
		ProcessIds:     m.ProcessIDs,
		RevisionRanges: ranges,
		Sha256:         m.SHA256,
		Sink:           m.Sink,
		Location:       m.Location,
		SourceNodeId:   m.SourceNodeID,
	}
}

func protoRestoreTargets(in []*procmeshv1.RestoreTarget) []backup.RestoreTarget {
	out := make([]backup.RestoreTarget, 0, len(in))
	for _, t := range in {
		if t == nil {
			continue
		}
		out = append(out, backup.RestoreTarget{
			ProcessID:        t.GetProcessId(),
			ExpectedRevision: t.GetExpectedRevision(),
		})
	}
	return out
}

func protoRestoreResults(in []backup.RestoreResult) []*procmeshv1.RestoreProcessResult {
	out := make([]*procmeshv1.RestoreProcessResult, 0, len(in))
	for _, r := range in {
		out = append(out, &procmeshv1.RestoreProcessResult{
			ProcessId:   r.ProcessID,
			Status:      r.Status,
			NewRevision: r.NewRevision,
			Error:       r.Error,
		})
	}
	return out
}

func applyBackupLimit(entries []*procmeshv1.BackupEntry, limit int) []*procmeshv1.BackupEntry {
	if limit <= 0 || len(entries) <= limit {
		return entries
	}
	var placeholders, rest []*procmeshv1.BackupEntry
	for _, e := range entries {
		if e != nil && e.GetSnapshot() == nil {
			placeholders = append(placeholders, e)
			continue
		}
		rest = append(rest, e)
	}
	keep := limit - len(placeholders)
	if keep < 0 {
		return placeholders[:limit]
	}
	if keep > len(rest) {
		keep = len(rest)
	}
	out := make([]*procmeshv1.BackupEntry, 0, keep+len(placeholders))
	out = append(out, rest[:keep]...)
	out = append(out, placeholders...)
	return out
}
