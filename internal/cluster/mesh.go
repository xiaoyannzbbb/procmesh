package cluster

import (
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/memberlist"
	"github.com/qleelulu/procmesh/internal/version"
)

type Config struct {
	NodeID    string
	BindAddr  string // default 127.0.0.1
	BindPort  int    // 0 = ephemeral（测试）
	Advertise string // host:port，可空
	Source    SummarySource
	Protocol  int         // must be version.Protocol
	Logger    *log.Logger // 可空
	TestFast  bool        // short probe/gossip intervals for tests
}

type Mesh struct {
	cfg       Config
	localName string
	bound     string
	list      *memberlist.Memberlist
	leaving   atomic.Bool

	mu        sync.RWMutex
	view      map[string]NodeSummary
	conflicts map[string]struct{}
}

func Start(cfg Config) (*Mesh, error) {
	if cfg.Source == nil {
		return nil, fmt.Errorf("cluster: Source is required")
	}
	if cfg.BindAddr == "" {
		cfg.BindAddr = "127.0.0.1"
	}
	if cfg.Protocol == 0 {
		cfg.Protocol = version.Protocol
	}

	snap := cfg.Source.Snapshot()
	if cfg.NodeID == "" {
		cfg.NodeID = snap.NodeID
	}
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("cluster: NodeID is required")
	}

	// nodeID#bootID so a VM clone with a new boot_id is not treated as the same memberlist node.
	localName := cfg.NodeID + "#" + snap.BootID

	m := &Mesh{
		cfg:       cfg,
		localName: localName,
		view:      make(map[string]NodeSummary),
		conflicts: make(map[string]struct{}),
	}

	conf := memberlist.DefaultLANConfig()
	if cfg.TestFast {
		conf = memberlist.DefaultLocalConfig()
		conf.ProbeInterval = 50 * time.Millisecond
		conf.ProbeTimeout = 25 * time.Millisecond
		conf.SuspicionMult = 1
		conf.GossipInterval = 20 * time.Millisecond
		conf.PushPullInterval = 100 * time.Millisecond
		conf.GossipToTheDeadTime = time.Second
		conf.TCPTimeout = 200 * time.Millisecond
	}
	conf.Name = localName
	conf.BindAddr = cfg.BindAddr
	conf.BindPort = cfg.BindPort
	conf.Delegate = m
	conf.Events = m
	if cfg.Logger != nil {
		conf.Logger = cfg.Logger
	} else {
		conf.LogOutput = io.Discard
	}

	if cfg.Advertise != "" {
		host, portStr, err := net.SplitHostPort(cfg.Advertise)
		if err != nil {
			return nil, fmt.Errorf("cluster: advertise: %w", err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("cluster: advertise port: %w", err)
		}
		conf.AdvertiseAddr = host
		conf.AdvertisePort = port
	}

	list, err := memberlist.Create(conf)
	if err != nil {
		return nil, fmt.Errorf("cluster: start memberlist: %w", err)
	}
	m.list = list
	ln := list.LocalNode()
	m.bound = net.JoinHostPort(ln.Addr.String(), strconv.Itoa(int(ln.Port)))
	// Refresh NodeMeta now that the bound port is known.
	_ = list.UpdateNode(0)
	return m, nil
}

func (m *Mesh) Join(seeds []string) (int, error) {
	if m.list == nil {
		return 0, fmt.Errorf("cluster: mesh not started")
	}
	return m.list.Join(seeds)
}

func (m *Mesh) Leave(timeout time.Duration) error {
	if m.list == nil {
		return nil
	}
	// Publish State=LEFT in NodeMeta before the leave broadcast.
	// memberlist v0.5.3 NotifyLeave passes &nodeState.Node; Node.State is
	// never written (nodeState.State shadows it) so peers cannot use n.State.
	m.leaving.Store(true)
	up := timeout
	if up <= 0 {
		up = 500 * time.Millisecond
	}
	_ = m.list.UpdateNode(up)
	return m.list.Leave(timeout)
}

func (m *Mesh) Shutdown() error {
	if m.list == nil {
		return nil
	}
	return m.list.Shutdown()
}

func (m *Mesh) LocalAddr() string {
	if m.bound != "" {
		return m.bound
	}
	if m.list == nil {
		return net.JoinHostPort(m.cfg.BindAddr, strconv.Itoa(m.cfg.BindPort))
	}
	n := m.list.LocalNode()
	return net.JoinHostPort(n.Addr.String(), strconv.Itoa(int(n.Port)))
}

func (m *Mesh) Update() {
	if m.list == nil {
		return
	}
	_ = m.list.UpdateNode(0)
}

// Members returns the local snapshot plus remote views, sorted by node_id.
func (m *Mesh) Members() []NodeSummary {
	local := m.localSummary()

	m.mu.RLock()
	out := make([]NodeSummary, 0, len(m.view)+1)
	for id, s := range m.view {
		if id == local.NodeID {
			continue
		}
		out = append(out, s)
	}
	m.mu.RUnlock()

	out = append(out, local)
	sort.Slice(out, func(i, j int) bool {
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

func (m *Mesh) NodeMeta(limit int) []byte {
	raw := EncodeMeta(m.localSummary())
	if limit > 0 && len(raw) > limit {
		s := m.localSummary()
		s.Labels = nil
		s.Resources = ResourceSummary{}
		raw = EncodeMeta(s)
	}
	return raw
}

func (m *Mesh) NotifyMsg([]byte) {}

func (m *Mesh) GetBroadcasts(overhead, limit int) [][]byte { return nil }

func (m *Mesh) LocalState(join bool) []byte {
	return EncodeState(m.localSummary())
}

func (m *Mesh) MergeRemoteState(buf []byte, join bool) {
	s, err := DecodeState(buf)
	if err != nil || s.NodeID == "" {
		return
	}
	m.store(s)
}

// MergeForTest applies the same merge path as MergeRemoteState.
func (m *Mesh) MergeForTest(buf []byte) {
	m.MergeRemoteState(buf, false)
}

// DuplicateConflicts returns node_ids whose remote boot_id collided with local.
func (m *Mesh) DuplicateConflicts() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.conflicts))
	for id := range m.conflicts {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (m *Mesh) NotifyJoin(n *memberlist.Node) {
	if n == nil || n.Name == m.localName {
		return
	}
	s := summaryFromNode(n)
	if s.State == "" {
		s.State = StateAlive
	}
	m.upsertMeta(s, true)
}

func (m *Mesh) NotifyLeave(n *memberlist.Node) {
	if n == nil || n.Name == m.localName {
		return
	}
	s := summaryFromNode(n)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rejectLocalCloneLocked(s) {
		return
	}
	prev := m.view[s.NodeID]
	s = mergePreserved(s, prev)
	// Do not trust n.State: memberlist v0.5.3 never assigns Node.State.
	if s.State == StateLeft || prev.State == StateLeft {
		s.State = StateLeft
	} else {
		s.State = StateFailed
	}
	m.view[s.NodeID] = s
}

func (m *Mesh) NotifyUpdate(n *memberlist.Node) {
	if n == nil || n.Name == m.localName {
		return
	}
	s := summaryFromNode(n)
	m.upsertMeta(s, false)
}

func (m *Mesh) localSummary() NodeSummary {
	var s NodeSummary
	if m.cfg.Source != nil {
		s = m.cfg.Source.Snapshot()
	}
	if s.NodeID == "" {
		s.NodeID = m.cfg.NodeID
	}
	if s.GossipAddress == "" {
		s.GossipAddress = m.LocalAddr()
	}
	if s.AgentVersion == "" {
		s.AgentVersion = version.Agent
	}
	if s.ProtocolVersion == 0 {
		s.ProtocolVersion = m.cfg.Protocol
		if s.ProtocolVersion == 0 {
			s.ProtocolVersion = version.Protocol
		}
	}
	if m.leaving.Load() {
		s.State = StateLeft
	}
	return s
}

// rejectLocalCloneLocked drops a remote with the local node_id.
// A different boot_id is recorded as a duplicate conflict.
func (m *Mesh) rejectLocalCloneLocked(s NodeSummary) bool {
	local := m.localSummary()
	if s.NodeID != local.NodeID {
		return false
	}
	if s.BootID != local.BootID {
		m.conflicts[s.NodeID] = struct{}{}
	}
	return true
}

func (m *Mesh) store(s NodeSummary) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rejectLocalCloneLocked(s) {
		return
	}
	if prev, ok := m.view[s.NodeID]; ok && keepTerminal(prev.State, s.State) {
		return
	}
	m.view[s.NodeID] = s
}

func (m *Mesh) upsertMeta(s NodeSummary, revive bool) {
	if s.NodeID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rejectLocalCloneLocked(s) {
		return
	}
	if prev, ok := m.view[s.NodeID]; ok {
		s = mergePreserved(s, prev)
		if !revive && keepTerminal(prev.State, s.State) {
			s.State = prev.State
		}
		if s.State == "" {
			s.State = prev.State
		}
	}
	if s.State == "" {
		s.State = StateAlive
	}
	m.view[s.NodeID] = s
}

func mergePreserved(s, prev NodeSummary) NodeSummary {
	if prev.NodeID == "" {
		return s
	}
	if len(s.Processes) == 0 {
		s.Processes = prev.Processes
	}
	if s.Resources == (ResourceSummary{}) {
		s.Resources = prev.Resources
	}
	if s.LastUpdatedUnixMs == 0 {
		s.LastUpdatedUnixMs = prev.LastUpdatedUnixMs
	}
	return s
}

// keepTerminal holds LEFT/FAILED against a late ALIVE snapshot.
// LEFT may replace FAILED; FAILED/LEFT are not revived except via NotifyJoin.
func keepTerminal(prev, incoming State) bool {
	switch prev {
	case StateLeft:
		return incoming != StateLeft
	case StateFailed:
		return incoming != StateLeft && incoming != StateFailed
	default:
		return false
	}
}

func summaryFromNode(n *memberlist.Node) NodeSummary {
	s, err := DecodeMeta(n.Meta)
	if err != nil || s.NodeID == "" {
		id, boot := splitMemberName(n.Name)
		s.NodeID = id
		if s.BootID == "" {
			s.BootID = boot
		}
	}
	if s.GossipAddress == "" {
		s.GossipAddress = net.JoinHostPort(n.Addr.String(), strconv.Itoa(int(n.Port)))
	}
	return s
}

func splitMemberName(name string) (nodeID, bootID string) {
	nodeID, bootID, ok := strings.Cut(name, "#")
	if !ok {
		return name, ""
	}
	return nodeID, bootID
}
