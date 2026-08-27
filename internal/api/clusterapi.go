package api

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/version"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

var _ procmeshv1connect.ClusterServiceHandler = (*ClusterAPI)(nil)

type ClusterDeps struct {
	Dir            string           // layout.ClusterDir
	Store          ClusterMetaStore // GetOrCreateNodeID, SetClusterID, GetClusterID
	Mesh           NodeLister       // Members(); nil → List uses Local
	Local          func() cluster.NodeSummary
	GossipAddr     func() string // local advertise, returned by seed Join
	Now            func() time.Time
	NodeID         string
	Hostname       string
	BootID         string
	APIAddr        string
	HTTPClient     *http.Client
	OnReady        func() error // called after Init/RequestJoin persist certs and before SetClusterID; failure is logged, Store must be attached
	Control        *control.Node
	ControlFn      func() *control.Node                // 晚绑定；优先于 Control
	RaftMembership control.RaftMembershipReader        // nil → use ControlFn/Control
	OnAdmit        func(nodeID, raftAddr string) error // leader AddNonvoter；nil 忽略
	LeaderAPI      func() string                       // 非 leader 转发 Join 的 API 地址
	RaftAddr       func() string                       // 本机 Raft advertise，RequestJoin 填入
	SetRaftLeader  func(addr string)                   // RequestJoin 记下 seed 返回的 leader
	RPCHealthy     func() bool                         // nil → true
	GossipHealthy  func() bool                         // nil → Mesh != nil
	CertExpires    func() int64                        // nil → parse agent.crt
	CAExpires      func() int64                        // nil → parse ca.crt
}

type ClusterMetaStore interface {
	GetOrCreateNodeID(ctx context.Context) (string, error)
	SetClusterID(ctx context.Context, id string) error
	GetClusterID(ctx context.Context) (string, error)
}

type NodeLister interface {
	Members() []cluster.NodeSummary
}

type meshJoiner interface {
	Join(seeds []string) (int, error)
}

type ClusterAPI struct {
	Deps     ClusterDeps
	Auth     *auth.Service
	Degraded func() bool
	Logger   *slog.Logger
}

func (s *ClusterAPI) Init(ctx context.Context, req *connect.Request[procmeshv1.InitClusterRequest]) (*connect.Response[procmeshv1.InitClusterResponse], error) {
	if err := rejectDegraded(s.Degraded); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := requireCluster(s.Deps); err != nil {
		return nil, err
	}
	nodeID, err := s.Deps.localNodeID(ctx)
	if err != nil {
		return nil, ToConnect(err)
	}
	result, err := control.Init(s.Deps.Dir, nodeID, req.Msg.GetAdminUsername(), s.Deps.now())
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := s.callOnReady(); err != nil {
		s.warn("cluster ready failed", err)
	}
	if s.Deps.Store != nil {
		if err := s.Deps.Store.SetClusterID(ctx, result.ClusterID); err != nil {
			return nil, ToConnect(err)
		}
	}
	return connect.NewResponse(&procmeshv1.InitClusterResponse{
		ClusterId:     result.ClusterID,
		NodeId:        result.NodeID,
		AdminUsername: result.AdminUser,
		AdminPassword: result.AdminPassword,
	}), nil
}

func (s *ClusterAPI) Join(ctx context.Context, req *connect.Request[procmeshv1.JoinClusterRequest]) (*connect.Response[procmeshv1.JoinClusterResponse], error) {
	if err := rejectDegraded(s.Degraded); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := requireCluster(s.Deps); err != nil {
		return nil, err
	}
	if err := requireInited(s.Deps.Dir); err != nil {
		return nil, err
	}
	adm := s.Deps.admission()
	// 先查 FSM 吊销：被删节点可能仍在 gossip 为 ALIVE，CheckJoin 会先撞 DUPLICATE_NODE_ID。
	if adm != nil && adm.IsRevoked(req.Msg.GetNodeId()) {
		return nil, ToConnect(errcode.E(errcode.DENIED, "node removed"))
	}
	if err := cluster.CheckJoin(s.Deps.members(), cluster.JoinIdentity{
		NodeID:          req.Msg.GetNodeId(),
		BootID:          req.Msg.GetBootId(),
		ProtocolVersion: int(req.Msg.GetProtocolVersion()),
	}); err != nil {
		return nil, ToConnect(err)
	}
	if adm != nil {
		if n := s.Deps.controlNode(); n != nil && !n.IsLeader() {
			return s.forwardJoin(ctx, req)
		}
	}
	now := s.Deps.now()
	meta, err := control.LoadMeta(s.Deps.Dir)
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := requireCAKey(s.Deps.Dir); err != nil {
		return nil, err
	}
	bundle, err := control.LoadBundle(s.Deps.Dir)
	if err != nil {
		return nil, ToConnect(err)
	}
	certPEM, err := control.SignCSR(bundle.CACertPEM, bundle.CAKeyPEM, req.Msg.GetCsrPem(), meta.ClusterID, req.Msg.GetNodeId(), now)
	if err != nil {
		return nil, ToConnect(err)
	}
	if adm != nil {
		if err := adm.ConsumeToken(req.Msg.GetToken(), now); err != nil {
			return nil, ToConnect(err)
		}
		serial, err := control.CertSerial(certPEM)
		if err != nil {
			return nil, ToConnect(err)
		}
		if err := adm.Admit(req.Msg.GetNodeId(), req.Msg.GetRaftAddress(), serial); err != nil {
			return nil, ToConnect(err)
		}
		if raftAddr := req.Msg.GetRaftAddress(); raftAddr != "" {
			add := s.Deps.OnAdmit
			if add == nil {
				if n := s.Deps.controlNode(); n != nil {
					add = n.AddNonvoter
				}
			}
			if add != nil {
				if err := add(req.Msg.GetNodeId(), raftAddr); err != nil {
					s.warn("add raft nonvoter failed", err)
				}
			}
		}
	} else {
		if err := control.ConsumeToken(s.Deps.Dir, req.Msg.GetToken(), now); err != nil {
			return nil, ToConnect(err)
		}
	}
	if gossip := req.Msg.GetGossipAddress(); gossip != "" {
		if err := control.AppendGossipSeed(s.Deps.Dir, gossip); err != nil {
			s.warn("persist gossip seed failed", err)
		}
	}
	resp := &procmeshv1.JoinClusterResponse{
		ClusterId:     meta.ClusterID,
		CaPem:         bundle.CACertPEM,
		CertPem:       certPEM,
		GossipAddress: s.Deps.gossipAddr(),
	}
	if n := s.Deps.controlNode(); n != nil {
		resp.RaftLeader = n.Advertise()
	}
	return connect.NewResponse(resp), nil
}

func (s *ClusterAPI) RequestJoin(ctx context.Context, req *connect.Request[procmeshv1.RequestJoinRequest]) (*connect.Response[procmeshv1.RequestJoinResponse], error) {
	if err := rejectDegraded(s.Degraded); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	if err := requireCluster(s.Deps); err != nil {
		return nil, err
	}
	if control.AlreadyInited(s.Deps.Dir) {
		return nil, ToConnect(errcode.E(errcode.CONFLICT, "cluster already initialized"))
	}
	if req.Msg.GetSeedServer() == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "seed_server required"))
	}
	if req.Msg.GetToken() == "" {
		return nil, ToConnect(errcode.E(errcode.INVALID, "token required"))
	}
	nodeID, err := s.Deps.localNodeID(ctx)
	if err != nil {
		return nil, ToConnect(err)
	}
	csrPEM, keyPEM, err := control.NewCSR("join", nodeID)
	if err != nil {
		return nil, ToConnect(err)
	}
	client := procmeshv1connect.NewClusterServiceClient(s.Deps.httpClient(), seedBaseURL(req.Msg.GetSeedServer()))
	joined, err := client.Join(ctx, connect.NewRequest(&procmeshv1.JoinClusterRequest{
		Meta:            req.Msg.GetMeta(),
		Token:           req.Msg.GetToken(),
		NodeId:          nodeID,
		Hostname:        s.Deps.Hostname,
		BootId:          s.Deps.BootID,
		ProtocolVersion: int32(version.Protocol),
		ApiAddress:      s.Deps.APIAddr,
		GossipAddress:   s.Deps.gossipAddr(),
		RaftAddress:     s.Deps.raftAddr(),
		CsrPem:          csrPEM,
	}))
	if err != nil {
		return nil, mapSeedErr(err)
	}
	now := s.Deps.now()
	meta := control.Meta{
		ClusterID:     joined.Msg.GetClusterId(),
		NodeID:        nodeID,
		ControlMember: false,
		CreatedAt:     now.UTC().Format(time.RFC3339),
	}
	if gossip := joined.Msg.GetGossipAddress(); gossip != "" {
		meta.GossipSeeds = []string{gossip}
	}
	if err := writeJoinerBundle(s.Deps.Dir, joined.Msg.GetCaPem(), joined.Msg.GetCertPem(), keyPEM, meta); err != nil {
		return nil, ToConnect(err)
	}
	if err := s.callOnReady(); err != nil {
		s.warn("cluster ready failed", err)
	}
	if s.Deps.Store != nil {
		if err := s.Deps.Store.SetClusterID(ctx, joined.Msg.GetClusterId()); err != nil {
			return nil, ToConnect(err)
		}
	}
	if s.Deps.SetRaftLeader != nil {
		s.Deps.SetRaftLeader(joined.Msg.GetRaftLeader())
	}
	if j, ok := s.Deps.Mesh.(meshJoiner); ok {
		if gossip := joined.Msg.GetGossipAddress(); gossip != "" {
			if _, err := j.Join([]string{gossip}); err != nil {
				s.warn("mesh join failed", err)
			}
		}
	}
	return connect.NewResponse(&procmeshv1.RequestJoinResponse{
		ClusterId:     joined.Msg.GetClusterId(),
		GossipAddress: joined.Msg.GetGossipAddress(),
	}), nil
}

func (s *ClusterAPI) warn(message string, err error) {
	if s.Logger != nil {
		s.Logger.Warn(message, "error", err)
	}
}

func (s *ClusterAPI) Overview(ctx context.Context, _ *connect.Request[procmeshv1.ClusterOverviewRequest]) (*connect.Response[procmeshv1.ClusterOverviewResponse], error) {
	if err := requirePerm(ctx, s.Auth, auth.PermClusterRead, "", false, true); err != nil {
		return nil, err
	}
	sum := summarize(s.Deps.members())
	var quorum bool
	var leader string
	if n := s.Deps.controlNode(); n != nil {
		quorum = n.HasQuorum()
		leader = n.LeaderAddr()
	}
	return connect.NewResponse(&procmeshv1.ClusterOverviewResponse{
		ClusterId:        s.Deps.clusterID(ctx),
		Members:          sum.members,
		Alive:            sum.alive,
		ControlQuorum:    quorum,
		ControlLeader:    leader,
		Suspect:          sum.suspect,
		Failed:           sum.failed,
		ProcessTotal:     sum.processTotal,
		ProcessRunning:   sum.processRunning,
		ProcessUnhealthy: sum.processUnhealthy,
		ProcessFatal:     sum.processFatal,
		CpuPercent:       sum.cpuPercent,
		MemoryPercent:    sum.memoryPercent,
		DiskPercent:      sum.diskPercent,
		GossipHealthy:    s.Deps.gossipHealthy(),
		RpcHealthy:       s.Deps.rpcHealthy(),
		AgentDegraded:    s.Degraded != nil && s.Degraded(),
		CertExpiresUnix:  s.Deps.certExpiresUnix(),
		CaExpiresUnix:    s.Deps.caExpiresUnix(),
		ViewUnixMs:       s.Deps.now().UnixMilli(),
		PlatformNote:     platformNote(),
		VersionCounts:    sum.versionCounts,
	}), nil
}

type overviewCounts struct {
	members, alive, suspect, failed                              int32
	processTotal, processRunning, processUnhealthy, processFatal int32
	cpuPercent, memoryPercent, diskPercent                       int32
	versionCounts                                                map[string]int32
}

func summarize(members []cluster.NodeSummary) overviewCounts {
	out := overviewCounts{
		members:       int32(len(members)),
		versionCounts: make(map[string]int32),
		cpuPercent:    unknownResourcePercent,
		memoryPercent: unknownResourcePercent,
		diskPercent:   unknownResourcePercent,
	}
	var cpuSum, memSum, diskSum, resN int
	for _, n := range members {
		switch n.State {
		case cluster.StateAlive:
			out.alive++
			if resourceCollected(n.Resources) {
				cpuSum += n.Resources.CPUPercent
				memSum += n.Resources.MemoryPercent
				diskSum += n.Resources.DiskPercent
				resN++
			}
		case cluster.StateSuspect:
			out.suspect++
		case cluster.StateFailed:
			out.failed++
		}
		out.processTotal += int32(len(n.Processes))
		for _, p := range n.Processes {
			if p.Observed == "RUNNING" {
				out.processRunning++
			}
			if p.Health == "UNHEALTHY" {
				out.processUnhealthy++
			}
			if p.Observed == "FATAL" {
				out.processFatal++
			}
		}
		ver := n.AgentVersion
		if ver == "" {
			ver = "unknown"
		}
		out.versionCounts[ver]++
	}
	if resN > 0 {
		out.cpuPercent = int32(cpuSum / resN)
		out.memoryPercent = int32(memSum / resN)
		out.diskPercent = int32(diskSum / resN)
	}
	return out
}

// unknownResourcePercent is the proto sentinel for "not collected".
const unknownResourcePercent int32 = -1

func resourceCollected(r cluster.ResourceSummary) bool {
	if r.CPUPercent < 0 || r.MemoryPercent < 0 || r.DiskPercent < 0 {
		return false
	}
	return r != (cluster.ResourceSummary{})
}

func protoResources(r cluster.ResourceSummary) *procmeshv1.ResourceSummary {
	if !resourceCollected(r) {
		return &procmeshv1.ResourceSummary{
			CpuPercent:          unknownResourcePercent,
			MemoryPercent:       unknownResourcePercent,
			DiskPercent:         unknownResourcePercent,
			HistoryWritesPaused: r.HistoryWritesPaused,
			HistoryPausePercent: int32(r.HistoryPausePercent),
		}
	}
	return &procmeshv1.ResourceSummary{
		CpuPercent:          int32(r.CPUPercent),
		MemoryPercent:       int32(r.MemoryPercent),
		DiskPercent:         int32(r.DiskPercent),
		HistoryWritesPaused: r.HistoryWritesPaused,
		HistoryPausePercent: int32(r.HistoryPausePercent),
	}
}

func platformNote() string {
	switch runtime.GOOS {
	case "linux":
		return ""
	case "darwin":
		return "macOS: resource_limit ignored (no cgroup); Host reboot recovery depends on how the Agent is started."
	default:
		return runtime.GOOS + ": linux process semantics unavailable"
	}
}

// CertNotAfterUnix returns NotAfter.Unix() of the named PEM cert under dir, or 0 on error.
func CertNotAfterUnix(dir, name string) int64 {
	if dir == "" {
		return 0
	}
	pemBytes, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return 0
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return 0
	}
	return cert.NotAfter.Unix()
}

func (d ClusterDeps) gossipHealthy() bool {
	if d.GossipHealthy != nil {
		return d.GossipHealthy()
	}
	return d.Mesh != nil
}

func (d ClusterDeps) rpcHealthy() bool {
	if d.RPCHealthy != nil {
		return d.RPCHealthy()
	}
	return true
}

func (d ClusterDeps) certExpiresUnix() int64 {
	if d.CertExpires != nil {
		return d.CertExpires()
	}
	return CertNotAfterUnix(d.Dir, "agent.crt")
}

func (d ClusterDeps) caExpiresUnix() int64 {
	if d.CAExpires != nil {
		return d.CAExpires()
	}
	return CertNotAfterUnix(d.Dir, "ca.crt")
}

func (s *ClusterAPI) callOnReady() error {
	if s.Deps.OnReady == nil {
		return nil
	}
	return s.Deps.OnReady()
}

func requireCluster(d ClusterDeps) error {
	if d.Dir == "" {
		return ToConnect(errcode.E(errcode.UNAVAILABLE, "cluster not configured"))
	}
	return nil
}

func requireInited(dir string) error {
	if !control.AlreadyInited(dir) {
		return ToConnect(errcode.E(errcode.INVALID, "cluster not initialized"))
	}
	return nil
}

func requireCAKey(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "ca.key")); err != nil {
		return ToConnect(errcode.E(errcode.UNAVAILABLE, "ca key not available"))
	}
	return nil
}

func requireCanIssueTokens(dir string) error {
	if err := requireInited(dir); err != nil {
		return err
	}
	meta, err := control.LoadMeta(dir)
	if err != nil {
		return ToConnect(err)
	}
	if !meta.ControlMember {
		return ToConnect(errcode.E(errcode.DENIED, "not a control member"))
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.key")); err != nil {
		return ToConnect(errcode.E(errcode.DENIED, "cluster CA key not available"))
	}
	return nil
}

func rejectDegraded(fn func() bool) error {
	if fn != nil && fn() {
		return ToConnect(errcode.E(errcode.DEGRADED, "degraded"))
	}
	return nil
}

func (d ClusterDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d ClusterDeps) localNodeID(ctx context.Context) (string, error) {
	if d.Store != nil {
		id, err := d.Store.GetOrCreateNodeID(ctx)
		if err != nil {
			return "", err
		}
		if id != "" {
			return id, nil
		}
	}
	if d.NodeID != "" {
		return d.NodeID, nil
	}
	return "", errcode.E(errcode.INVALID, "node_id required")
}

func (d ClusterDeps) members() []cluster.NodeSummary {
	if d.Mesh != nil {
		return d.Mesh.Members()
	}
	if d.Local != nil {
		return []cluster.NodeSummary{d.Local()}
	}
	return nil
}

func (d ClusterDeps) gossipAddr() string {
	if d.GossipAddr != nil {
		if a := d.GossipAddr(); a != "" {
			return a
		}
	}
	if d.Local != nil {
		return d.Local().GossipAddress
	}
	return ""
}

func (d ClusterDeps) raftAddr() string {
	if d.RaftAddr != nil {
		return d.RaftAddr()
	}
	return ""
}

func (d ClusterDeps) controlNode() *control.Node {
	if d.ControlFn != nil {
		if n := d.ControlFn(); n != nil {
			return n
		}
	}
	return d.Control
}

func (d ClusterDeps) raftMembershipReader() control.RaftMembershipReader {
	if d.RaftMembership != nil {
		return d.RaftMembership
	}
	return d.controlNode()
}

func (d ClusterDeps) admission() *control.Admission {
	n := d.controlNode()
	if n == nil {
		return nil
	}
	return &control.Admission{Node: n}
}

func (s *ClusterAPI) forwardJoin(ctx context.Context, req *connect.Request[procmeshv1.JoinClusterRequest]) (*connect.Response[procmeshv1.JoinClusterResponse], error) {
	var leaderAPI string
	if s.Deps.LeaderAPI != nil {
		leaderAPI = s.Deps.LeaderAPI()
	}
	if leaderAPI == "" || sameAPIAddr(leaderAPI, s.Deps.APIAddr) {
		return nil, ToConnect(errcode.E(errcode.UNAVAILABLE, "raft leader api unknown"))
	}
	client := procmeshv1connect.NewClusterServiceClient(s.Deps.httpClient(), seedBaseURL(leaderAPI))
	resp, err := client.Join(ctx, connect.NewRequest(req.Msg))
	if err != nil {
		return nil, mapJoinForwardErr(err)
	}
	return resp, nil
}

func mapJoinForwardErr(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch ce.Code() {
		case connect.CodeUnavailable, connect.CodeUnknown, connect.CodeDeadlineExceeded:
			return ToConnect(errcode.E(errcode.UNAVAILABLE, "leader unreachable"))
		default:
			return err
		}
	}
	return ToConnect(errcode.E(errcode.UNAVAILABLE, "leader unreachable"))
}

func (d ClusterDeps) clusterID(ctx context.Context) string {
	if d.Store != nil {
		id, err := d.Store.GetClusterID(ctx)
		if err == nil && id != "" {
			return id
		}
	}
	if d.Dir != "" {
		if meta, err := control.LoadMeta(d.Dir); err == nil {
			return meta.ClusterID
		}
	}
	if d.Local != nil {
		return d.Local().ClusterID
	}
	return ""
}

func (d ClusterDeps) httpClient() *http.Client {
	if d.HTTPClient != nil {
		return d.HTTPClient
	}
	return http.DefaultClient
}

func seedBaseURL(seed string) string {
	if strings.Contains(seed, "://") {
		return seed
	}
	return "http://" + seed
}

func sameAPIAddr(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.TrimPrefix(seedBaseURL(a), "http://") == strings.TrimPrefix(seedBaseURL(b), "http://")
}

func mapSeedErr(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch ce.Code() {
		case connect.CodeUnavailable, connect.CodeUnknown, connect.CodeDeadlineExceeded:
			return ToConnect(errcode.E(errcode.UNAVAILABLE, "seed unreachable"))
		default:
			return err
		}
	}
	return ToConnect(errcode.E(errcode.UNAVAILABLE, "seed unreachable"))
}

// writeJoinerBundle persists CA + agent cert/key and cluster.json.
// It must not write ca.key or the cluster secret.
func writeJoinerBundle(dir string, caPEM, certPEM, keyPEM []byte, meta control.Meta) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
		perm os.FileMode
	}{
		{"ca.crt", caPEM, 0o640},
		{"agent.crt", certPEM, 0o640},
		{"agent.key", keyPEM, 0o600},
	}
	for _, f := range files {
		if err := writePerm(filepath.Join(dir, f.name), f.data, f.perm); err != nil {
			return err
		}
	}
	doc, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return writePerm(filepath.Join(dir, "cluster.json"), append(doc, '\n'), 0o640)
}

func writePerm(path string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}
