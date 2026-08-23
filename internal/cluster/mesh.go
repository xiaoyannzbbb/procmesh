package cluster

import (
	"fmt"
	"io"
	"log"
	"log/slog"
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

// DefaultSuspectAfter is how long an ALIVE peer may go without LastUpdated
// before Members() overlays StateSuspect. This is the mesh overlay, not the
// AGENT_SUSPECT_TOO_LONG alert window (control.AlertPolicy.SuspectTooLongSec).
const DefaultSuspectAfter = 2 * time.Second

// DefaultUpdateTimeout bounds a metadata publication so the Agent's
// reconcile loop cannot be blocked indefinitely by Gossip dissemination.
const DefaultUpdateTimeout = 2 * time.Second

type Config struct {
	NodeID            string
	BindAddr          string // default 127.0.0.1
	BindPort          int    // 0 = ephemeral（测试）
	Advertise         string // host:port，可空
	Source            SummarySource
	Protocol          int          // must be version.Protocol
	Logger            *slog.Logger // 可空
	TestFast          bool         // short probe/gossip intervals for tests
	EnableCompression bool         // false avoids memberlist's high per-packet LZW allocation
	// SuspectAfter overlays StateSuspect on still-present ALIVE remotes whose
	// LastUpdatedUnixMs is this old. Zero means DefaultSuspectAfter (2s).
	SuspectAfter time.Duration
	// UpdateTimeout bounds UpdateNode dissemination. Zero means
	// DefaultUpdateTimeout (2s).
	UpdateTimeout time.Duration
	Now           func() time.Time
}

type Mesh struct {
	cfg       Config
	localName string
	localBoot string
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
		localBoot: snap.BootID,
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
	conf.EnableCompression = cfg.EnableCompression
	conf.BindAddr = cfg.BindAddr
	conf.BindPort = cfg.BindPort
	conf.Delegate = m
	conf.Events = m
	if cfg.Logger != nil {
		conf.Logger = newMemberlistLogger(cfg.Logger)
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
	if err := list.UpdateNode(m.updateTimeout()); err != nil && cfg.Logger != nil {
		cfg.Logger.Error("initial gossip metadata publish failed",
			"observer_node_id", cfg.NodeID,
			"timeout_ms", m.updateTimeout().Milliseconds(),
			"error", err,
		)
	}
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
	started := time.Now()
	timeout := m.updateTimeout()
	if m.cfg.Logger != nil {
		m.cfg.Logger.Debug("gossip metadata publish started",
			"observer_node_id", m.cfg.NodeID,
			"timeout_ms", timeout.Milliseconds(),
		)
	}
	slow := time.AfterFunc(timeout, func() {
		if m.cfg.Logger != nil {
			m.cfg.Logger.Warn("gossip metadata publish slow",
				"observer_node_id", m.cfg.NodeID,
				"duration_ms", time.Since(started).Milliseconds(),
				"timeout_ms", timeout.Milliseconds(),
			)
		}
	})
	err := m.list.UpdateNode(timeout)
	slow.Stop()
	if m.cfg.Logger == nil {
		return
	}
	fields := []any{
		"observer_node_id", m.cfg.NodeID,
		"duration_ms", time.Since(started).Milliseconds(),
		"timeout_ms", timeout.Milliseconds(),
	}
	if err != nil {
		m.cfg.Logger.Error("gossip metadata publish failed", append(fields, "error", err)...)
		return
	}
	m.cfg.Logger.Debug("gossip metadata publish completed", fields...)
}

// ApplyMemberlistState maps a memberlist node state onto the local view.
// StateSuspect becomes cluster.StateSuspect and does not overlay LEFT/REMOVED/REVOKED/FAILED.
func (m *Mesh) ApplyMemberlistState(nodeID string, state memberlist.NodeStateType) {
	if nodeID == "" || state != memberlist.StateSuspect {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.markSuspectLocked(nodeID) && m.cfg.Logger != nil {
		m.cfg.Logger.Warn("gossip member marked suspect",
			"reason", "memberlist_suspect",
			"observer_node_id", m.cfg.NodeID,
			"node_id", nodeID,
		)
	}
}

// Members returns the local snapshot plus remote views, sorted by node_id.
func (m *Mesh) Members() []NodeSummary {
	m.applyMemberlistStates()
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

func (m *Mesh) applyMemberlistStates() {
	present := m.presentMemberIDs()
	if len(present) == 0 {
		return
	}
	now := m.now()
	after := m.suspectAfter()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range present {
		s, ok := m.view[id]
		if !ok {
			continue
		}
		switch s.State {
		case StateLeft, StateRemoved, StateRevoked, StateFailed:
			continue
		}
		if s.LastUpdatedUnixMs == 0 {
			continue
		}
		if now.Sub(time.UnixMilli(s.LastUpdatedUnixMs)) < after {
			continue
		}
		if m.markSuspectLocked(id) && m.cfg.Logger != nil {
			m.cfg.Logger.Warn("gossip member marked suspect",
				"reason", "metadata_stale",
				"observer_node_id", m.cfg.NodeID,
				"node_id", id,
				"metadata_age_ms", now.Sub(time.UnixMilli(s.LastUpdatedUnixMs)).Milliseconds(),
				"threshold_ms", after.Milliseconds(),
				"last_updated_unix_ms", s.LastUpdatedUnixMs,
			)
		}
	}
}

func (m *Mesh) presentMemberIDs() map[string]struct{} {
	out := map[string]struct{}{}
	if m.list == nil {
		return out
	}
	for _, n := range m.list.Members() {
		if n == nil {
			continue
		}
		id, _ := splitMemberName(n.Name)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func (m *Mesh) now() time.Time {
	if m.cfg.Now != nil {
		return m.cfg.Now()
	}
	return time.Now()
}

func (m *Mesh) suspectAfter() time.Duration {
	if m.cfg.SuspectAfter > 0 {
		return m.cfg.SuspectAfter
	}
	return DefaultSuspectAfter
}

func (m *Mesh) updateTimeout() time.Duration {
	if m.cfg.UpdateTimeout > 0 {
		return m.cfg.UpdateTimeout
	}
	return DefaultUpdateTimeout
}

func (m *Mesh) markSuspectLocked(nodeID string) bool {
	prev, ok := m.view[nodeID]
	if !ok {
		return false
	}
	switch prev.State {
	case StateLeft, StateRemoved, StateRevoked, StateFailed:
		return false
	case StateSuspect:
		return false
	}
	prev.State = StateSuspect
	m.view[nodeID] = prev
	return true
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
	m.store(s, "push_pull")
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
	m.upsertMeta(s, true, "join")
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
	if m.cfg.Logger != nil {
		logFn := m.cfg.Logger.Warn
		if s.State == StateLeft {
			logFn = m.cfg.Logger.Info
		}
		logFn("gossip member left",
			"observer_node_id", m.cfg.NodeID,
			"node_id", s.NodeID,
			"previous_state", prev.State,
			"state", s.State,
		)
	}
}

func (m *Mesh) NotifyUpdate(n *memberlist.Node) {
	if n == nil || n.Name == m.localName {
		return
	}
	s := summaryFromNode(n)
	m.upsertMeta(s, false, "node_meta")
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
	if s.NodeID != m.cfg.NodeID {
		return false
	}
	if s.BootID != m.localBoot {
		m.conflicts[s.NodeID] = struct{}{}
	}
	return true
}

func (m *Mesh) store(s NodeSummary, source string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rejectLocalCloneLocked(s) {
		return
	}
	prev := m.view[s.NodeID]
	if prev.NodeID != "" && keepTerminal(prev.State, s.State) {
		return
	}
	m.view[s.NodeID] = s
	m.logRemoteUpdateLocked(prev, s, source)
}

func (m *Mesh) upsertMeta(s NodeSummary, revive bool, source string) {
	if s.NodeID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rejectLocalCloneLocked(s) {
		return
	}
	prev := m.view[s.NodeID]
	if prev.NodeID != "" {
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
	m.logRemoteUpdateLocked(prev, s, source)
}

func (m *Mesh) logRemoteUpdateLocked(prev, current NodeSummary, source string) {
	if m.cfg.Logger == nil {
		return
	}
	ageMs := int64(-1)
	if current.LastUpdatedUnixMs > 0 {
		ageMs = m.now().Sub(time.UnixMilli(current.LastUpdatedUnixMs)).Milliseconds()
	}
	m.cfg.Logger.Debug("gossip metadata received",
		"observer_node_id", m.cfg.NodeID,
		"node_id", current.NodeID,
		"source", source,
		"previous_state", prev.State,
		"state", current.State,
		"metadata_age_ms", ageMs,
		"last_updated_unix_ms", current.LastUpdatedUnixMs,
	)
	if prev.State == StateSuspect && current.State == StateAlive {
		m.cfg.Logger.Info("gossip member recovered",
			"observer_node_id", m.cfg.NodeID,
			"node_id", current.NodeID,
			"source", source,
			"previous_state", prev.State,
			"state", current.State,
			"metadata_age_ms", ageMs,
		)
		return
	}
	if prev.NodeID == "" {
		m.cfg.Logger.Info("gossip member joined",
			"observer_node_id", m.cfg.NodeID,
			"node_id", current.NodeID,
			"source", source,
			"state", current.State,
		)
	}
}

type memberlistLogWriter struct {
	logger *slog.Logger
}

func newMemberlistLogger(logger *slog.Logger) *log.Logger {
	return log.New(memberlistLogWriter{logger: logger}, "", 0)
}

func (w memberlistLogWriter) Write(p []byte) (int, error) {
	message := strings.TrimSpace(string(p))
	logFn := w.logger.Info
	for _, level := range []struct {
		prefix string
		fn     func(string, ...any)
	}{
		{prefix: "[DEBUG]", fn: w.logger.Debug},
		{prefix: "[WARN]", fn: w.logger.Warn},
		{prefix: "[ERR]", fn: w.logger.Error},
		{prefix: "[ERROR]", fn: w.logger.Error},
		{prefix: "[INFO]", fn: w.logger.Info},
	} {
		if strings.HasPrefix(message, level.prefix) {
			message = strings.TrimSpace(strings.TrimPrefix(message, level.prefix))
			logFn = level.fn
			break
		}
	}
	logFn(message, "source", "memberlist")
	return len(p), nil
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
