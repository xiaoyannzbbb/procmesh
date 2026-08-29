package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/agentcfg"
	"github.com/qleelulu/procmesh/internal/alert"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/batch"
	"github.com/qleelulu/procmesh/internal/breakglass"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/identity"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/metrics"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/version"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

const defaultGossipListen = "127.0.0.1:18689"
const defaultRPCListen = "127.0.0.1:18683"

const (
	diskProtectEvery    = 5 * time.Second
	logRotateEvery      = 10 * time.Second
	backupScheduleEvery = 5 * time.Second
)

type agentLoopWork struct {
	diskProtect    bool
	logRotate      bool
	backupSchedule bool
}

type agentLoopCadence struct {
	lastDiskProtect    time.Time
	lastLogRotate      time.Time
	lastBackupSchedule time.Time
}

// runTriggers exposes deterministic maintenance triggers to same-package tests
// without widening the runtime configuration interface used by production callers.
type runTriggers struct {
	agentLoop                <-chan struct{}
	afterAgentLoop           func()
	alertScan                <-chan struct{}
	afterAlertScan           func()
	runtimeReady             func(*rpcRuntime)
	afterBackupRunClaim      func()
	beforeBackupTaskDispatch func(backup.BackupTaskRequest)
	manualBackupCoordinator  bool
}

func (c *agentLoopCadence) due(now time.Time) agentLoopWork {
	return agentLoopWork{
		diskProtect:    dueAt(now, &c.lastDiskProtect, diskProtectEvery),
		logRotate:      dueAt(now, &c.lastLogRotate, logRotateEvery),
		backupSchedule: dueAt(now, &c.lastBackupSchedule, backupScheduleEvery),
	}
}

func dueAt(now time.Time, last *time.Time, interval time.Duration) bool {
	if !last.IsZero() && now.Before(last.Add(interval)) {
		return false
	}
	*last = now
	return true
}

// Options is the procmesh-agent runtime configuration.
type Options struct {
	DataDir            string
	Listen             string
	PprofListen        string
	ShimBin            string
	InsecureListen     bool
	OnListen           func(addr string)
	OnPprofListen      func(addr string)
	ConfigPath         string
	Logger             *slog.Logger
	GossipListen       string // default 127.0.0.1:18689
	GossipAdvertise    string
	RPCListen          string // default 127.0.0.1:18683; tests use 127.0.0.1:0
	RPCAdvertise       string
	OnRPCListen        func(addr string)
	ControlListen      string // default 127.0.0.1:18685; tests use 127.0.0.1:0
	ControlAdvertise   string
	OnControlListen    func(addr string)
	BreakGlassSocket   string
	BreakGlassGroup    string
	OnBreakGlassListen func(path string)
	BootID             string // empty = paths.CurrentBootID(); tests may override
	Backup             agentcfg.Backup
	DiskPercent        func() float64 // optional override for backup disk protection (tests)
	Now                func() time.Time
	triggers           *runTriggers
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Run owns the agent lifecycle and blocks until ctx is cancelled.
func Run(ctx context.Context, opt Options) error {
	logger := opt.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	opt.Logger = logger

	path := opt.ConfigPath
	required := path != ""
	if path == "" {
		path = agentcfg.DefaultPath()
	}
	cfg, err := agentcfg.LoadAll(path, required)
	if err != nil {
		return err
	}
	if opt.DataDir == "" {
		opt.DataDir = cfg.DataDir
	}
	if opt.DataDir == "" {
		return fmt.Errorf("data-dir required")
	}
	if opt.Listen == "" {
		opt.Listen = cfg.Listen
	}
	if opt.PprofListen == "" {
		opt.PprofListen = cfg.Pprof.Listen
	}
	if opt.BreakGlassSocket == "" {
		opt.BreakGlassSocket = cfg.BreakGlass.Socket
	}
	if opt.BreakGlassSocket == "" {
		opt.BreakGlassSocket = breakglass.DefaultSocketPath(opt.DataDir)
	}
	if opt.BreakGlassGroup == "" {
		opt.BreakGlassGroup = cfg.BreakGlass.Group
	}

	logger.Info("agent starting", "data_dir", opt.DataDir)
	if opt.Listen == "" {
		opt.Listen = "127.0.0.1:18680"
	}
	if err := CheckListen(opt.Listen, opt.InsecureListen); err != nil {
		return err
	}
	logInsecureListen(logger, opt.Listen, opt.InsecureListen)
	if opt.PprofListen != "" {
		if err := CheckListen(opt.PprofListen, opt.InsecureListen); err != nil {
			return fmt.Errorf("pprof %w", err)
		}
		logInsecureListen(logger.With("component", "pprof"), opt.PprofListen, opt.InsecureListen)
	}

	layout := paths.New(opt.DataDir)
	if err := layout.Ensure(); err != nil {
		return fmt.Errorf("ensure layout: %w", err)
	}

	degraded := false
	st, err := store.Open(layout.Store)
	if err != nil {
		quarantine := layout.Store + ".corrupt-" + strconv.FormatInt(time.Now().Unix(), 10)
		if rerr := os.Rename(layout.Store, quarantine); rerr != nil {
			logger.Warn("store quarantine failed", "error", rerr, "open_error", err)
		} else {
			logger.Warn("store quarantined", "path", quarantine, "error", err)
		}
		st, err = store.Open(layout.Store)
		if err != nil {
			logger.Warn("store reopen failed", "error", err)
			return serveHTTP(ctx, opt, nil, nil, nil, true, func() error {
				return errcode.E(errcode.DEGRADED, "store unavailable")
			}, nil, nil, nil, api.ClusterDeps{}, nil)
		}
		degraded = true
	}
	defer func() { _ = st.Close() }()

	health := newStoreHealthMonitor(
		st.IntegrityCheck,
		st.QuickCheck,
		func() error { return checkSQLiteFileHeader(layout.Store) },
	)
	if err := health.initialize(ctx); err != nil {
		degraded = true
		logger.Warn("store integrity check failed", "error", err)
	}

	hostBoot := opt.BootID
	if hostBoot == "" {
		hostBoot = paths.CurrentBootID()
	}
	if err := st.SetBootID(ctx, hostBoot); err != nil {
		return fmt.Errorf("set boot id: %w", err)
	}
	if _, err := identity.Ensure(ctx, layout, st, hostBoot); err != nil {
		return fmt.Errorf("ensure identity: %w", err)
	}

	logs := &logmgr.Manager{Root: layout.Root, Now: time.Now, Policy: cfg.Disk}
	mgr := process.NewManager(process.Deps{
		Store:    st,
		Layout:   layout,
		ShimBin:  opt.ShimBin,
		Now:      time.Now,
		LookUser: lookupUser,
		Logs:     logs,
		Logger:   logger,
	})
	if err := mgr.Recover(ctx); err != nil {
		logger.Warn("process recovery failed", "error", err)
	}
	if err := mgr.Reconcile(ctx); err != nil {
		logger.Warn("process reconcile failed", "error", err)
	}

	collector := metrics.New(layout.Root, 5*time.Second)
	if err := collector.Start(ctx); err != nil {
		return fmt.Errorf("start metrics collector: %w", err)
	}

	gossipListen := opt.GossipListen
	if gossipListen == "" {
		gossipListen = cfg.Gossip.Listen
	}
	if gossipListen == "" {
		gossipListen = defaultGossipListen
	}
	gossipAdvertise := opt.GossipAdvertise
	if gossipAdvertise == "" {
		gossipAdvertise = cfg.Gossip.Advertise
	}
	if err := CheckListen(gossipListen, opt.InsecureListen); err != nil {
		return err
	}
	logInsecureListen(logger, gossipListen, opt.InsecureListen)
	gossipAdvertise, err = resolveAdvertiseAddr(gossipListen, gossipAdvertise)
	if err != nil {
		return fmt.Errorf("gossip advertise: %w", err)
	}
	if opt.RPCListen == "" {
		opt.RPCListen = cfg.RPC.Listen
	}
	if opt.RPCListen == "" {
		opt.RPCListen = defaultRPCListen
	}
	if opt.RPCAdvertise == "" {
		opt.RPCAdvertise = cfg.RPC.Advertise
	}
	if err := CheckListen(opt.RPCListen, opt.InsecureListen); err != nil {
		return err
	}
	logInsecureListen(logger, opt.RPCListen, opt.InsecureListen)
	opt.RPCAdvertise, err = resolveAdvertiseAddr(opt.RPCListen, opt.RPCAdvertise)
	if err != nil {
		return fmt.Errorf("rpc advertise: %w", err)
	}
	if opt.ControlListen == "" {
		opt.ControlListen = cfg.Control.Listen
	}
	if opt.ControlListen == "" {
		opt.ControlListen = defaultControlListen
	}
	if opt.ControlAdvertise == "" {
		opt.ControlAdvertise = cfg.Control.Advertise
	}
	if err := CheckListen(opt.ControlListen, opt.InsecureListen); err != nil {
		return err
	}
	logInsecureListen(logger, opt.ControlListen, opt.InsecureListen)
	bindCtrl, advCtrl, err := resolveControlAddr(opt.ControlListen, opt.ControlAdvertise)
	if err != nil {
		return fmt.Errorf("control address: %w", err)
	}
	opt.ControlListen = bindCtrl
	opt.ControlAdvertise = advCtrl
	bindAddr, bindPort, err := splitListen(gossipListen)
	if err != nil {
		return err
	}

	nodeID, err := st.GetOrCreateNodeID(ctx)
	if err != nil {
		return fmt.Errorf("node id: %w", err)
	}
	rec := metrics.NewRecorder(st, nodeID)
	rec.CollectNode = collector.NodeMetrics
	rec.CollectProcess = collector.ProcessMetrics
	rec.ListProcesses = func() []metrics.ProcessRef {
		return listProcessRefs(mgr)
	}
	rec.DiskPercent = func() float64 {
		nm, err := collector.NodeMetrics()
		if err != nil || nm == nil {
			return 0
		}
		return nm.DiskPercent
	}
	rec.PauseWrites = func(diskPercent float64) bool {
		return historyWritesPaused(cfg.Disk, diskPercent)
	}
	_ = rec.Start(ctx)
	defer rec.Stop()
	hostname, _ := os.Hostname()
	src := &liveSource{
		nodeID:     nodeID,
		hostname:   hostname,
		bootID:     hostBoot,
		store:      st,
		mgr:        mgr,
		metrics:    collector,
		diskPolicy: cfg.Disk,
		process:    cfg.Process,
	}

	if control.AlreadyInited(layout.ClusterDir) || agentCertExists(layout.ClusterDir) {
		// Joiners persist agent.crt without ca.key; skip LoadBundle unless the seed CA key is present.
		if _, err := os.Stat(filepath.Join(layout.ClusterDir, "ca.key")); err == nil {
			if _, err := control.LoadBundle(layout.ClusterDir); err != nil {
				logger.Warn("cluster bundle load failed", "error", err)
			}
		}
	}

	mesh, err := cluster.Start(cluster.Config{
		NodeID:            nodeID,
		BindAddr:          bindAddr,
		BindPort:          bindPort,
		Advertise:         gossipAdvertise,
		Source:            src,
		Protocol:          version.Protocol,
		TestFast:          bindPort == 0,
		EnableCompression: cfg.Gossip.Compression,
		Logger:            logger.With("component", "gossip"),
	})
	if err != nil {
		return fmt.Errorf("start mesh: %w", err)
	}
	src.setGossip(mesh.LocalAddr())
	logger.With("component", "gossip").Info("gossip listening", "address", mesh.LocalAddr(), "compression", cfg.Gossip.Compression)
	if meta, err := control.LoadMeta(layout.ClusterDir); err == nil && len(meta.GossipSeeds) > 0 {
		members, err := mesh.Join(meta.GossipSeeds)
		if err != nil {
			logger.Warn("gossip rejoin failed", "error", err)
		} else {
			logger.Info("gossip rejoined", "members", members, "seeds", len(meta.GossipSeeds))
		}
	}

	healthCtx, stopHealth := context.WithCancel(ctx)
	defer stopHealth()
	go health.runPeriodic(healthCtx)
	ready := health.ready
	batchEng := newBatchEngine(st, cfg, nodeID)
	opt.Backup = mergeBackupConfig(cfg.Backup, opt.Backup)
	return serveHTTP(ctx, opt, mgr, logs, st, degraded, ready, mesh, src, collector, api.ClusterDeps{
		Dir:        layout.ClusterDir,
		Store:      st,
		Mesh:       mesh,
		Local:      src.Snapshot,
		GossipAddr: mesh.LocalAddr,
		NodeID:     nodeID,
		Hostname:   hostname,
		BootID:     hostBoot,
	}, batchEng)
}

func listProcessRefs(mgr *process.Manager) []metrics.ProcessRef {
	if mgr == nil {
		return nil
	}
	specs, err := mgr.ListSpecs(context.Background())
	if err != nil {
		return nil
	}
	var out []metrics.ProcessRef
	for _, spec := range specs {
		insts, err := mgr.ListInstances(context.Background(), spec.ProcessID)
		if err != nil {
			continue
		}
		for _, inst := range insts {
			if inst.PID <= 0 {
				continue
			}
			out = append(out, metrics.ProcessRef{
				ProcessID: spec.ProcessID,
				PID:       inst.PID,
			})
		}
	}
	return out
}

func mergeBackupConfig(file, override agentcfg.Backup) agentcfg.Backup {
	out := file
	if override.FSDir != "" {
		out.FSDir = override.FSDir
	}
	if override.Schedule != "" {
		out.Schedule = override.Schedule
	}
	if override.S3.Bucket != "" || override.S3.Endpoint != "" {
		out.S3 = override.S3
	}
	if len(override.S3Profiles) > 0 {
		out.S3Profiles = override.S3Profiles
	}
	return out
}

func newBackupEngine(opt Options, mgr *process.Manager, st *store.Store, collector *metrics.Collector, rt *rpcRuntime, fwd *agentForwarder) *backup.Engine {
	if st == nil || opt.DataDir == "" {
		return nil
	}
	layout := paths.New(opt.DataDir)
	fsDir := opt.Backup.FSDir
	if fsDir == "" {
		fsDir = layout.BackupFSDir()
	}
	sinks := map[string]backup.Sink{
		"fs":                   backup.NewFSSink(fsDir),
		backup.ReplicaSinkName: backup.NewFSSink(layout.BackupReplicaDir()),
	}
	destinations := make(map[string]backup.Sink, len(opt.Backup.S3Profiles))
	clusterID, _ := st.GetClusterID(context.Background())
	if opt.Backup.S3.Bucket != "" {
		s3, err := backup.NewS3Sink(backup.S3Config{
			Endpoint:  opt.Backup.S3.Endpoint,
			Bucket:    opt.Backup.S3.Bucket,
			Prefix:    opt.Backup.S3.Prefix,
			Region:    opt.Backup.S3.Region,
			AccessKey: opt.Backup.S3.AccessKey,
			SecretKey: opt.Backup.S3.SecretKey,
			Insecure:  opt.Backup.S3.Insecure,
			ClusterID: clusterID,
			NodeID:    rt.nodeID,
		})
		if err != nil {
			opt.Logger.Warn("s3 backup sink disabled", "error", err)
		} else {
			sinks["s3"] = s3
		}
	}
	for name, profile := range opt.Backup.S3Profiles {
		s3, err := backup.NewS3Sink(backup.S3Config{
			Endpoint:  profile.Endpoint,
			Bucket:    profile.Bucket,
			Prefix:    profile.Prefix,
			Region:    profile.Region,
			AccessKey: profile.AccessKey,
			SecretKey: profile.SecretKey,
			Insecure:  profile.Insecure,
			ClusterID: clusterID,
			NodeID:    rt.nodeID,
		})
		if err != nil {
			opt.Logger.Warn("s3 backup profile disabled", "profile", name, "error", err)
			continue
		}
		destinations[name] = s3
	}
	return &backup.Engine{
		Store:     st,
		NodeID:    rt.nodeID,
		ClusterID: clusterID,
		Now:       opt.Now,
		Apply:     mgr,
		Sinks:     sinks,
		OnRetentionDelete: func(ctx context.Context, ev backup.RetentionDeleteEvent) {
			api.ObserveRetentionDelete(ctx, st, rt.nodeID, ev)
		},
		ResolveDestination: func(profile string) (backup.Sink, error) {
			sink, ok := destinations[profile]
			if !ok {
				return nil, errcode.E(errcode.INVALID, "destination profile not configured")
			}
			return sink, nil
		},
		RetentionPolicy: func(policyID string) (backup.Policy, bool) {
			n := rt.control()
			if n == nil {
				return backup.Policy{}, false
			}
			view := n.View()
			if policy, ok := view.BackupPolicies[policyID]; ok {
				return backup.PolicyFromRecord(backup.PolicyRecord{
					PolicyID: policy.PolicyID, Name: policy.Name, Enabled: policy.Enabled,
					ScheduleCron: policy.ScheduleCron, Timezone: policy.Timezone,
					TargetSelector: policy.TargetSelector, TargetIDs: policy.TargetIDs,
					Sink: policy.Sink, DestinationProfile: policy.DestinationProfile,
					RetentionKeepLast: policy.RetentionKeepLast, RetentionKeepDays: policy.RetentionKeepDays,
					RetentionMaxBytes: policy.RetentionMaxBytes, TimeoutSeconds: policy.TimeoutSeconds,
					MaxConcurrency: policy.MaxConcurrency, UnavailablePolicy: policy.UnavailablePolicy,
					Revision: policy.Revision,
				}), true
			}
			rp, ok := view.ReplicationPolicies[policyID]
			if !ok {
				return backup.Policy{}, false
			}
			return backup.Policy{
				PolicyID: rp.PolicyID, Name: rp.Name, Enabled: rp.Enabled,
				ScheduleCron: rp.ScheduleCron, Timezone: rp.Timezone,
				Sink: backup.ReplicaSinkName, RetentionKeepLast: rp.RetentionKeepLast,
				RetentionKeepDays: rp.RetentionKeepDays, RetentionMaxBytes: rp.RetentionMaxBytes,
				MaxConcurrency: rp.MaxConcurrency, Revision: rp.Revision,
			}, true
		},
		PeerStore: &backup.PeerStore{Root: opt.DataDir},
		PeerPush: backup.PeerPushFunc(func(ctx context.Context, nodeID, sourceNodeID string, payload []byte) error {
			return pushPeerSnapshot(ctx, fwd, rt, nodeID, sourceNodeID, payload)
		}),
		ReplicationPush: backup.ReplicationPeerPushFunc(func(ctx context.Context, request backup.ReplicationPushRequest, payload []byte) error {
			return pushReplicationSnapshot(ctx, fwd, rt, request, payload)
		}),
		Admitted: func(nodeID string) bool {
			n := rt.control()
			if n == nil {
				return false
			}
			view := n.View()
			m, ok := view.Member(nodeID)
			return ok && m.Status == control.MemberAdmitted
		},
		DiskPercent: func() float64 {
			if opt.DiskPercent != nil {
				return opt.DiskPercent()
			}
			if collector == nil {
				return 0
			}
			nm, err := collector.NodeMetrics()
			if err != nil || nm == nil {
				return 0
			}
			return nm.DiskPercent
		},
		Schedule: opt.Backup.Schedule,
	}
}

func pushReplicationSnapshot(ctx context.Context, fwd *agentForwarder, rt *rpcRuntime, request backup.ReplicationPushRequest, payload []byte) error {
	if fwd == nil || rt == nil {
		return errcode.E(errcode.UNAVAILABLE, "replication peer push not configured")
	}
	addr := ""
	for _, member := range rt.memberList() {
		if member.NodeID == request.TargetNodeID {
			addr = member.RPCAddress
			break
		}
	}
	if addr == "" {
		return errcode.E(errcode.UNAVAILABLE, "replication target unavailable")
	}
	client, err := fwd.PeerReplication(ctx, api.Route{NodeID: request.TargetNodeID, RPC: addr})
	if err != nil {
		return err
	}
	_, err = client.PutSnapshot(ctx, connect.NewRequest(&procmeshv1.PutSnapshotRequest{ClusterId: rt.clusterID, SnapshotId: request.SnapshotID, Sha256: request.SHA256, RunId: request.RunID, TaskId: request.TaskID, Payload: payload, PolicyId: request.PolicyID, PolicyRevision: request.PolicyRevision}))
	return err
}

func pushPeerSnapshot(ctx context.Context, fwd *agentForwarder, rt *rpcRuntime, nodeID, sourceNodeID string, payload []byte) error {
	if fwd == nil {
		return errcode.E(errcode.UNAVAILABLE, "peer push not configured")
	}
	addr := ""
	if rt != nil {
		for _, m := range rt.memberList() {
			if m.NodeID == nodeID {
				addr = m.RPCAddress
				break
			}
		}
	}
	cli, err := fwd.Backup(ctx, api.Route{NodeID: nodeID, RPC: addr})
	if err != nil {
		return err
	}
	req := connect.NewRequest(&procmeshv1.PutPeerSnapshotRequest{
		Meta:         &procmeshv1.MutationMeta{OperationId: newBatchID(), Operator: "peer"},
		SourceNodeId: sourceNodeID,
		Payload:      payload,
	})
	if rt != nil {
		rpc.SetSource(req.Header(), rt.nodeID)
	}
	rpc.SetTarget(req.Header(), nodeID)
	if p, ok := api.PrincipalFrom(ctx); ok {
		src := make(http.Header)
		rpc.SetUserID(src, p.UserID)
		if p.SessionID != "" {
			rpc.SetSessionID(src, p.SessionID)
		} else if p.TokenID != "" {
			rpc.SetTokenID(src, p.TokenID)
		}
		rpc.CopyIdentity(req.Header(), src)
	}
	req.Header().Del("Authorization")
	_, err = cli.PutPeerSnapshot(ctx, req)
	return err
}

func (r *rpcRuntime) memberList() []cluster.NodeSummary {
	if r == nil {
		return nil
	}
	if r.mesh != nil {
		return r.mesh.Members()
	}
	if r.src != nil {
		return []cluster.NodeSummary{r.src.Snapshot()}
	}
	return nil
}

func processRemotePolicy(src *liveSource) api.ProcessRemotePolicy {
	if src == nil {
		return api.ProcessRemotePolicy{}
	}
	return api.ProcessRemotePolicy{
		DisableCreate: src.process.DisableRemoteCreate,
		DisableUpdate: src.process.DisableRemoteUpdate,
		DisableDelete: src.process.DisableRemoteDelete,
	}
}

func newBatchEngine(st *store.Store, cfg agentcfg.Config, nodeID string) *batch.Engine {
	if st == nil {
		return nil
	}
	return &batch.Engine{
		DB:            st,
		Concurrency:   cfg.Batch.MaxConcurrency,
		TargetTimeout: cfg.Batch.TargetTimeout,
		SourceAgent:   nodeID,
		NewID:         newBatchID,
	}
}

func newBatchID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func serveHTTP(ctx context.Context, opt Options, mgr *process.Manager, logs *logmgr.Manager, st *store.Store, degraded bool, ready func() error, mesh *cluster.Mesh, src *liveSource, collector *metrics.Collector, clusterDeps api.ClusterDeps, batchEng *batch.Engine) error {
	ln, err := net.Listen("tcp", opt.Listen)
	if err != nil {
		shutdownMesh(mesh)
		return fmt.Errorf("listen: %w", err)
	}
	apiAddr := ln.Addr().String()
	if src != nil {
		src.setAPI(apiAddr)
	}
	clusterDeps.APIAddr = apiAddr
	if mesh != nil {
		mesh.Update()
	}

	fwd := &agentForwarder{}
	authSvc := &auth.Service{}
	started := time.Now()
	raftDir := ""
	if clusterDeps.Dir != "" {
		raftDir = filepath.Join(filepath.Dir(clusterDeps.Dir), "raft")
	}
	rt := &rpcRuntime{
		ctx:         ctx,
		opt:         opt,
		dir:         clusterDeps.Dir,
		nodeID:      clusterDeps.NodeID,
		mgr:         mgr,
		st:          st,
		mesh:        mesh,
		src:         src,
		ready:       ready,
		degraded:    degraded,
		fwd:         fwd,
		node:        clusterDeps.Control,
		auth:        authSvc,
		raftDir:     raftDir,
		controlBind: opt.ControlListen,
		controlAdv:  opt.ControlAdvertise,
		started:     started,
		logger:      opt.Logger,
		metrics:     collector,
		process:     processRemotePolicy(src),
	}
	rt.backup = newBackupEngine(opt, mgr, st, collector, rt, fwd)
	rt.backupCoord = backup.NewCoordinator(backup.CoordinatorConfig{
		Control:    raftBackupControl{runtime: rt},
		Dispatcher: localBackupDispatcher{runtime: rt},
		RunCreator: raftBackupRunCreator{runtime: rt},
		IsLeader:   func() bool { n := rt.control(); return n != nil && n.IsLeader() },
		CurrentTerm: func() uint64 {
			n := rt.control()
			if n == nil {
				return 0
			}
			return n.CurrentTerm()
		},
		Now: opt.Now,
	})
	rt.replicationCoord = backup.NewReplicationCoordinator(backup.ReplicationCoordinatorConfig{
		Control: raftReplicationControl{runtime: rt}, Dispatcher: localReplicationDispatcher{runtime: rt},
		IsLeader: func() bool { n := rt.control(); return n != nil && n.IsLeader() },
		CurrentTerm: func() uint64 {
			n := rt.control()
			if n == nil {
				return 0
			}
			return n.CurrentTerm()
		},
		Now: opt.Now,
	})
	if opt.triggers != nil && opt.triggers.runtimeReady != nil {
		opt.triggers.runtimeReady(rt)
	}
	if control.AlreadyInited(clusterDeps.Dir) || raftLogExists(raftDir) {
		if err := rt.startRaft(false); err != nil {
			_ = ln.Close()
			rt.shutdown(context.Background())
			shutdownMesh(mesh)
			return fmt.Errorf("start raft: %w", err)
		}
		clusterDeps.Control = rt.control()
	}
	clusterDeps.ControlFn = rt.control
	clusterDeps.OnAdmit = rt.onAdmit
	clusterDeps.LeaderAPI = rt.leaderAPI
	clusterDeps.RaftAddr = rt.raftAddr
	clusterDeps.SetRaftLeader = rt.setKnownLeader
	clusterDeps.OnReady = rt.onReady
	if err := rt.startRPC(); err != nil {
		_ = ln.Close()
		rt.shutdown(context.Background())
		shutdownMesh(mesh)
		return fmt.Errorf("start rpc: %w", err)
	}

	var members func() []cluster.NodeSummary
	if mesh != nil {
		members = mesh.Members
	} else if src != nil {
		members = func() []cluster.NodeSummary { return []cluster.NodeSummary{src.Snapshot()} }
	}
	router := &api.Router{
		LocalID:      clusterDeps.NodeID,
		LocalHost:    clusterDeps.Hostname,
		Members:      members,
		LocalHasName: localHasNameFn(mgr),
		ControlStatus: func(nodeID string) (string, bool) {
			n := rt.control()
			if n == nil {
				return "", false
			}
			view := n.View()
			m, ok := view.Member(nodeID)
			if !ok {
				return "", false
			}
			return string(m.Status), true
		},
	}

	var revs api.RevisionStore
	if st != nil {
		revs = st
	}
	srv, err := api.NewServer(api.Options{
		Addr:             opt.Listen,
		Logger:           opt.Logger.With("component", "http"),
		Mgr:              mgr,
		Logs:             logs,
		Store:            revs,
		Cluster:          clusterDeps,
		Auth:             authSvc,
		Degraded:         degraded,
		Ready:            ready,
		Started:          started,
		LocalOnly:        false,
		LocalID:          clusterDeps.NodeID,
		Router:           router,
		Forward:          fwd,
		LoginLeaderRoute: rt.leaderRoute,
		HasQuorum: func() bool {
			n := rt.control()
			return n != nil && n.HasQuorum()
		},
		RPCHealthy:    rt.rpcListening,
		GossipHealthy: func() bool { return mesh != nil },
		CertExpires:   func() int64 { return api.CertNotAfterUnix(clusterDeps.Dir, "agent.crt") },
		CAExpires:     func() int64 { return api.CertNotAfterUnix(clusterDeps.Dir, "ca.crt") },
		Members:       members,
		Metrics:       collector,
		Batch:         batchEng,
		Backup:        rt.backup,
		BackupDispatch: func(run backup.FrozenRun) {
			go rt.backupCoord.DispatchRun(ctx, run)
		},
		ReplicationDispatch: func(run backup.FrozenReplicationRun) {
			go rt.replicationCoord.DispatchRun(ctx, run)
		},
		Process: rt.process,
	})
	if err != nil {
		_ = ln.Close()
		rt.shutdown(context.Background())
		shutdownMesh(mesh)
		return fmt.Errorf("new server: %w", err)
	}
	var breakGlassServer *breakglass.Server
	if mgr != nil && st != nil {
		breakGlassServer, err = breakglass.New(breakglass.Config{
			SocketPath: opt.BreakGlassSocket,
			Group:      opt.BreakGlassGroup,
			LocalID:    clusterDeps.NodeID,
			Manager:    mgr,
			Audit:      st,
			Recovery: func() breakglass.RecoveryStore {
				return rt.control()
			},
		})
		if err != nil {
			_ = ln.Close()
			rt.shutdown(context.Background())
			shutdownMesh(mesh)
			return fmt.Errorf("start break-glass: %w", err)
		}
	}
	pprofServer, err := newPprofServer(opt.PprofListen)
	if err != nil {
		_ = ln.Close()
		if breakGlassServer != nil {
			_ = breakGlassServer.Shutdown(context.Background())
		}
		rt.shutdown(context.Background())
		shutdownMesh(mesh)
		return err
	}

	if batchEng != nil && !degraded {
		batchEng.Start(ctx)
		if err := batchEng.Resume(ctx); err != nil {
			opt.Logger.Warn("batch resume failed", "error", err)
		}
	}

	opt.Logger.With("component", "http").Info("http listening", "address", apiAddr)
	if pprofServer != nil {
		opt.Logger.With("component", "pprof").Info("pprof listening", "address", pprofServer.Addr())
	}
	opt.Logger.Info("agent started")
	if opt.OnListen != nil {
		opt.OnListen(apiAddr)
	}
	if breakGlassServer != nil && opt.OnBreakGlassListen != nil {
		opt.OnBreakGlassListen(opt.BreakGlassSocket)
	}
	if pprofServer != nil && opt.OnPprofListen != nil {
		opt.OnPprofListen(pprofServer.Addr())
	}

	if mgr != nil {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			var manual <-chan struct{}
			if opt.triggers != nil {
				manual = opt.triggers.agentLoop
			}
			var cadence agentLoopCadence
			var lastBackupMin time.Time
			runRound := func(force bool) {
				now := opt.now()
				work := cadence.due(now)
				if force {
					work = agentLoopWork{diskProtect: true, logRotate: true, backupSchedule: true}
				}
				if err := mgr.Reconcile(ctx); err != nil {
					opt.Logger.Warn("process reconcile failed", "error", err)
				}
				if logs != nil && work.diskProtect {
					logs.ExtraLogDirs = mgr.CustomLogDirs(ctx)
					if _, err := logs.Protect(ctx); err != nil {
						opt.Logger.Warn("disk protection failed", "error", err)
					}
				}
				if work.logRotate {
					_ = mgr.RotateLogs(ctx)
				}
				if mesh != nil {
					mesh.Update()
				}
				if bak := rt.backup; bak != nil && work.backupSchedule {
					if rt.backupCoord != nil && (opt.triggers == nil || !opt.triggers.manualBackupCoordinator) {
						if err := rt.backupCoord.Tick(ctx); err != nil && ctx.Err() == nil {
							opt.Logger.Warn("cluster backup schedule tick failed", "error", err)
						}
					}
					if rt.replicationCoord != nil {
						if err := rt.replicationCoord.Tick(ctx); err != nil && ctx.Err() == nil {
							opt.Logger.Warn("replication schedule tick failed", "error", err)
						}
					}
					min := now.Truncate(time.Minute)
					if lastBackupMin.IsZero() || !min.Equal(lastBackupMin) {
						lastBackupMin = min
						clusterEnabled := false
						clusterPolicyReadOK := false
						if rt.backupCoord != nil {
							if policies, err := (raftBackupControl{runtime: rt}).ListEnabledBackupPolicies(ctx); err == nil {
								clusterPolicyReadOK = true
								clusterEnabled = len(policies) > 0
							}
						}
						if clusterPolicyReadOK && !clusterEnabled {
							if err := bak.TickSchedule(ctx); err != nil && ctx.Err() == nil {
								opt.Logger.Warn("backup schedule tick failed", "error", err)
							}
						}
					}
				}
			}
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					runRound(false)
				case _, ok := <-manual:
					if !ok {
						manual = nil
						continue
					}
					runRound(true)
					if opt.triggers.afterAgentLoop != nil {
						opt.triggers.afterAgentLoop()
					}
				}
			}
		}()
	}

	startAlertScanner(ctx, opt, mgr, st, mesh, collector, authSvc, rt, clusterDeps)

	errCh := make(chan error, 3)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	if breakGlassServer != nil {
		go func() {
			errCh <- breakGlassServer.Serve()
		}()
	}
	if pprofServer != nil {
		go func() {
			errCh <- pprofServer.Serve()
		}()
	}

	select {
	case <-ctx.Done():
		opt.Logger.Info("agent stopping", "reason", ctx.Err().Error())
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		if breakGlassServer != nil {
			_ = breakGlassServer.Shutdown(shutCtx)
		}
		if pprofServer != nil {
			_ = pprofServer.Shutdown(shutCtx)
		}
		rt.shutdown(shutCtx)
		shutdownMesh(mesh)
		if collector != nil {
			collector.Stop()
		}
		opt.Logger.Info("agent stopped")
		return nil
	case err := <-errCh:
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		if breakGlassServer != nil {
			_ = breakGlassServer.Shutdown(shutCtx)
		}
		if pprofServer != nil {
			_ = pprofServer.Shutdown(shutCtx)
		}
		rt.shutdown(shutCtx)
		shutdownMesh(mesh)
		if collector != nil {
			collector.Stop()
		}
		return err
	}
}

func shutdownMesh(mesh *cluster.Mesh) {
	if mesh == nil {
		return
	}
	_ = mesh.Leave(time.Second)
	_ = mesh.Shutdown()
}

func agentCertExists(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "agent.crt"))
	return err == nil && !st.IsDir()
}

func splitListen(addr string) (host string, port int, err error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("gossip address: %w", err)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("gossip port: %w", err)
	}
	return host, port, nil
}

// resolveAdvertiseAddr supplies the listen port when advertise names only a host.
// An explicit port, including :0, keeps its existing behavior.
func resolveAdvertiseAddr(listen, advertise string) (string, error) {
	if advertise == "" {
		return "", nil
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("listen address: %w", err)
	}

	advertiseHost, advertisePort, err := net.SplitHostPort(advertise)
	if err == nil {
		if _, parseErr := strconv.ParseUint(advertisePort, 10, 16); parseErr != nil {
			return "", fmt.Errorf("port %q: %w", advertisePort, parseErr)
		}
		if advertisePort != "0" {
			return advertise, nil
		}
		return net.JoinHostPort(advertiseHost, port), nil
	}

	if ip := net.ParseIP(advertise); ip != nil {
		return net.JoinHostPort(ip.String(), port), nil
	}
	if strings.HasPrefix(advertise, "[") && strings.HasSuffix(advertise, "]") {
		if ip := net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(advertise, "["), "]")); ip != nil {
			return net.JoinHostPort(ip.String(), port), nil
		}
	}
	if !strings.Contains(advertise, ":") {
		return net.JoinHostPort(advertise, port), nil
	}
	return "", fmt.Errorf("address %q: %w", advertise, err)
}

// CheckListen refuses non-loopback binds unless insecure is set.
func CheckListen(addr string, insecure bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	if host == "" {
		// ":18680" binds all interfaces
		if !insecure {
			return errcode.E(errcode.INVALID, "non-loopback listen requires --insecure-listen")
		}
		return nil
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if loopback {
		return nil
	}
	if !insecure {
		return errcode.E(errcode.INVALID, "non-loopback listen requires --insecure-listen")
	}
	return nil
}

func logInsecureListen(logger *slog.Logger, addr string, insecure bool) {
	if !insecure {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return
	}
	logger.Warn("insecure listen", "address", addr)
}

func lookupUser(name string) error {
	if name == "" {
		return nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return errcode.E(errcode.INVALID, "run_as_user")
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return errcode.E(errcode.INVALID, "run_as_user")
	}
	if uid != os.Getuid() && os.Getuid() != 0 {
		return errcode.E(errcode.INVALID, "run_as_user")
	}
	return nil
}

const alertScanInterval = 15 * time.Second

func startAlertScanner(ctx context.Context, opt Options, mgr *process.Manager, st *store.Store, mesh *cluster.Mesh, collector *metrics.Collector, authSvc *auth.Service, rt *rpcRuntime, clusterDeps api.ClusterDeps) {
	if st == nil {
		return
	}
	eng := &alert.Engine{
		Store:  st,
		NodeID: clusterDeps.NodeID,
		NewID:  newBatchID,
		Policy: func() control.AlertPolicy {
			if authSvc == nil || authSvc.Store() == nil {
				return control.DefaultAlertPolicy()
			}
			return authSvc.Store().View().AlertPolicy
		},
		Channels: func() []control.AlertChannel {
			if authSvc == nil || authSvc.Store() == nil {
				return nil
			}
			m := authSvc.Store().View().AlertChannels
			out := make([]control.AlertChannel, 0, len(m))
			for _, ch := range m {
				out = append(out, ch)
			}
			return out
		},
		Sender: &alert.ChannelSender{},
		OnSendError: func(ch control.AlertChannel, err error) {
			opt.Logger.Warn("alert notification failed", "channel_id", ch.ChannelID, "channel_type", ch.Type, "error", err)
		},
		Audit: func(action, result, meta string) {
			payload, err := json.Marshal(map[string]string{"channel_id": meta})
			if err != nil {
				payload = []byte(`{"channel_id":""}`)
			}
			_ = st.AppendAudit(context.Background(), store.AuditEvent{
				Action:   action,
				Result:   result,
				Resource: meta,
				Metadata: payload,
			})
		},
	}
	hostname, _ := os.Hostname()
	sc := &alert.Scanner{
		Engine:    eng,
		NodeID:    clusterDeps.NodeID,
		Hostname:  hostname,
		ListProcs: func() []alert.ProcessSnap { return listAlertProcessSnaps(mgr) },
		Samples:   st.ListMetricSamples,
		Snapshot:  func() alert.NodeSample { return alertNodeSample(collector) },
		Degraded:  rt.degradedFn(),
	}
	go func() {
		ticker := time.NewTicker(alertScanInterval)
		defer ticker.Stop()
		var manual <-chan struct{}
		if opt.triggers != nil {
			manual = opt.triggers.alertScan
		}
		scan := func() {
			if err := sc.ScanLocal(ctx); err != nil && ctx.Err() == nil {
				opt.Logger.Warn("alert scan local failed", "error", err)
			}
			if err := sc.ScanCluster(ctx, buildAlertClusterView(st, mesh, rt, clusterDeps)); err != nil && ctx.Err() == nil {
				opt.Logger.Warn("alert scan cluster failed", "error", err)
			}
		}
		scan()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				scan()
			case _, ok := <-manual:
				if !ok {
					manual = nil
					continue
				}
				scan()
				if opt.triggers.afterAlertScan != nil {
					opt.triggers.afterAlertScan()
				}
			}
		}
	}()
}

func listAlertProcessSnaps(mgr *process.Manager) []alert.ProcessSnap {
	if mgr == nil {
		return nil
	}
	specs, err := mgr.ListSpecs(context.Background())
	if err != nil {
		return nil
	}
	var out []alert.ProcessSnap
	for _, spec := range specs {
		insts, err := mgr.ListInstances(context.Background(), spec.ProcessID)
		if err != nil || len(insts) == 0 {
			continue
		}
		chosen := insts[0]
		for _, inst := range insts {
			if inst.Ordinal == 0 {
				chosen = inst
				break
			}
		}
		for _, inst := range insts {
			if instanceFiresAlert(inst) {
				chosen = inst
				break
			}
		}
		out = append(out, alert.ProcessSnap{
			ProcessID: spec.ProcessID,
			Desired:   string(chosen.Desired),
			Observed:  string(chosen.Observed),
			Health:    string(chosen.Health),
		})
	}
	return out
}

func instanceFiresAlert(inst process.Instance) bool {
	if inst.Observed == process.ObservedFatal {
		return true
	}
	if inst.Desired != process.DesiredRunning {
		return false
	}
	switch inst.Observed {
	case process.ObservedExited, process.ObservedBackoff:
		return true
	case process.ObservedRunning:
		return inst.Health == process.HealthUnhealthy
	default:
		return false
	}
}

func alertNodeSample(collector *metrics.Collector) alert.NodeSample {
	if collector == nil {
		return alert.NodeSample{}
	}
	nm, err := collector.NodeMetrics()
	if err != nil || nm == nil {
		return alert.NodeSample{}
	}
	return alert.NodeSample{
		CPUPercent:       nm.CPUPercent,
		MemoryPercent:    nm.MemoryPercent,
		DiskPercent:      nm.DiskPercent,
		MemoryTotalBytes: int64(nm.MemoryTotal),
		HaveSnapshot:     true,
	}
}

func buildAlertClusterView(st *store.Store, mesh *cluster.Mesh, rt *rpcRuntime, clusterDeps api.ClusterDeps) alert.ClusterView {
	view := alert.ClusterView{}
	if st != nil {
		if id, err := st.GetClusterID(context.Background()); err == nil {
			view.ClusterID = id
		}
	}
	if n := rt.control(); n != nil {
		view.Leader = n.IsLeader()
		view.Voter = n.IsVoter()
		view.HasQuorum = n.HasQuorum()
		view.LeaderAddr = n.LeaderAddr()
	}
	if mesh != nil {
		view.Members = mesh.Members()
	}
	if unix := api.CertNotAfterUnix(clusterDeps.Dir, "agent.crt"); unix > 0 {
		view.CertNotAfter = map[string]time.Time{clusterDeps.NodeID: time.Unix(unix, 0)}
	}
	return view
}
