package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"github.com/qleelulu/procmesh/internal/errcode"
	"go.etcd.io/bbolt"
)

const (
	raftDBFile   = "raft.db"
	snapDBFile   = "snapshots.bolt"
	snapBucket   = "snaps"
	snapRetain   = 3
	membershipTO = 10 * time.Second
	notLeaderMsg = "not raft leader"
	// QuorumContactProd is a short heartbeat window, separate from DefaultRBACCacheTTL.
	QuorumContactProd = 10 * time.Second
	QuorumContactTest = 2 * time.Second
)

type RaftConfig struct {
	Dir       string // $data_dir/raft
	Bind      string // 127.0.0.1:18685 或 127.0.0.1:0
	Advertise string // 空则用实际 bind
	NodeID    string
	ClusterID string
}

type Node struct {
	raft          *raft.Raft
	fsm           *FSM
	advertise     string
	id            string
	clusterID     string
	quorumContact time.Duration

	closers   []io.Closer
	closeOnce sync.Once
	closeErr  error
}

type RaftSuffrage string

const (
	RaftVoter    RaftSuffrage = "VOTER"
	RaftNonVoter RaftSuffrage = "NON_VOTER"
)

// RaftMembershipView is a request-level snapshot without Raft addresses or
// HashiCorp-specific types, so API consumers cannot expose control-plane peers.
type RaftMembershipView struct {
	Members   map[string]RaftSuffrage
	LeaderID  string
	HasQuorum bool
}

type RaftMembershipReader interface {
	RaftMembershipView() (RaftMembershipView, error)
}

func Start(cfg RaftConfig) (*Node, error) {
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("raft dir: %w", err)
	}

	store, err := raftboltdb.NewBoltStore(filepath.Join(cfg.Dir, raftDBFile))
	if err != nil {
		return nil, fmt.Errorf("raft.db: %w", err)
	}

	snaps, err := openBoltSnapshotStore(filepath.Join(cfg.Dir, snapDBFile))
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("snapshots.bolt: %w", err)
	}

	var advertise net.Addr
	if cfg.Advertise != "" {
		tcpAddr, err := net.ResolveTCPAddr("tcp", cfg.Advertise)
		if err != nil {
			_ = snaps.Close()
			_ = store.Close()
			return nil, fmt.Errorf("advertise: %w", err)
		}
		advertise = tcpAddr
	}
	trans, err := raft.NewTCPTransport(cfg.Bind, advertise, 3, 10*time.Second, io.Discard)
	if err != nil {
		_ = snaps.Close()
		_ = store.Close()
		return nil, fmt.Errorf("raft transport: %w", err)
	}

	adv := cfg.Advertise
	if adv == "" {
		adv = string(trans.LocalAddr())
	}

	fsm := NewFSM()
	rconf := raft.DefaultConfig()
	rconf.LocalID = raft.ServerID(cfg.NodeID)
	rconf.LogOutput = io.Discard
	r, err := raft.NewRaft(rconf, fsm, store, store, snaps, trans)
	if err != nil {
		_ = trans.Close()
		_ = snaps.Close()
		_ = store.Close()
		return nil, err
	}

	return &Node{
		raft:          r,
		fsm:           fsm,
		advertise:     adv,
		id:            cfg.NodeID,
		clusterID:     cfg.ClusterID,
		quorumContact: QuorumContactProd,
		closers:       []io.Closer{trans, snaps, store},
	}, nil
}

// StartInmem starts a test-only Raft node with in-memory log and snapshots.
func StartInmem(nodeID string, fsm *FSM, trans raft.Transport) (*Node, error) {
	if fsm == nil {
		fsm = NewFSM()
	}
	store := raft.NewInmemStore()
	snaps := raft.NewInmemSnapshotStore()
	rconf := raft.DefaultConfig()
	rconf.LocalID = raft.ServerID(nodeID)
	rconf.HeartbeatTimeout = 100 * time.Millisecond
	rconf.ElectionTimeout = 100 * time.Millisecond
	rconf.LeaderLeaseTimeout = 100 * time.Millisecond
	rconf.CommitTimeout = 10 * time.Millisecond
	rconf.LogOutput = io.Discard
	r, err := raft.NewRaft(rconf, fsm, store, store, snaps, trans)
	if err != nil {
		return nil, err
	}
	return &Node{
		raft:          r,
		fsm:           fsm,
		advertise:     string(trans.LocalAddr()),
		id:            nodeID,
		quorumContact: QuorumContactTest,
	}, nil
}

func (n *Node) Bootstrap() error {
	cfg := raft.Configuration{
		Servers: []raft.Server{{
			Suffrage: raft.Voter,
			ID:       raft.ServerID(n.id),
			Address:  raft.ServerAddress(n.advertise),
		}},
	}
	return n.raft.BootstrapCluster(cfg).Error()
}

func (n *Node) Apply(cmd Command, timeout time.Duration) error {
	if err := n.requireLeader(); err != nil {
		return err
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	f := n.raft.Apply(data, timeout)
	if err := f.Error(); err != nil {
		return mapWriteErr(err)
	}
	if resp := f.Response(); resp != nil {
		if e, ok := resp.(error); ok {
			return e
		}
	}
	return nil
}

// ClaimBackupFire applies an idempotent scheduled-fire claim and returns the
// durable run identity recorded by the FSM. The created flag is false when an
// existing live claim is reused.
func (n *Node) ClaimBackupFire(b FireClaimBody, now time.Time) (FireRecord, bool, error) {
	if err := n.requireLeader(); err != nil {
		return FireRecord{}, false, err
	}
	before := n.View().BackupFireLedger[b.FireKey]
	cmd, err := EncodeCommand(CmdBackupFireClaim, b)
	if err != nil {
		return FireRecord{}, false, err
	}
	if err := n.Apply(cmd, 5*time.Second); err != nil {
		return FireRecord{}, false, err
	}
	after, ok := n.View().BackupFireLedger[b.FireKey]
	if !ok {
		return FireRecord{}, false, errcode.E(errcode.UNAVAILABLE, "fire claim not committed")
	}
	// created reports only first insertion. Lease takeover changes the durable
	// claim term but must not cause the coordinator to recreate the run.
	created := before.RunID == ""
	return after, created, nil
}

// ClaimScheduledBackupRun atomically claims a scheduled fire and writes its
// frozen run. acquired is false while a live lease belongs to another leader.
func (n *Node) ClaimScheduledBackupRun(b ScheduledRunClaimBody, now time.Time) (FireRecord, ClusterBackupRun, bool, error) {
	if err := n.requireLeader(); err != nil {
		return FireRecord{}, ClusterBackupRun{}, false, err
	}
	before, existed := n.View().BackupFireLedger[b.Fire.FireKey]
	cmd, err := EncodeCommand(CmdBackupScheduledRunClaim, b)
	if err != nil {
		return FireRecord{}, ClusterBackupRun{}, false, err
	}
	if err := n.Apply(cmd, 5*time.Second); err != nil {
		return FireRecord{}, ClusterBackupRun{}, false, err
	}
	state := n.View()
	record, ok := state.BackupFireLedger[b.Fire.FireKey]
	if !ok {
		return FireRecord{}, ClusterBackupRun{}, false, errcode.E(errcode.UNAVAILABLE, "scheduled fire not committed")
	}
	run, ok := state.BackupRuns[record.RunID]
	if !ok {
		return FireRecord{}, ClusterBackupRun{}, false, errcode.E(errcode.UNAVAILABLE, "scheduled run not committed")
	}
	acquired := !existed || record.LeaderTerm != before.LeaderTerm || record.ClaimedUnix != before.ClaimedUnix
	return record, run, acquired, nil
}

func (n *Node) HasQuorum() bool {
	if n == nil || n.raft == nil {
		return false
	}
	if n.IsLeader() {
		return n.raft.VerifyLeader().Error() == nil
	}
	if n.LeaderAddr() == "" {
		return false
	}
	last := n.LastContact()
	if last.IsZero() {
		return false
	}
	window := n.quorumContact
	if window <= 0 {
		window = QuorumContactProd
	}
	return time.Since(last) < window
}

func (n *Node) RaftMembershipView() (RaftMembershipView, error) {
	if n == nil || n.raft == nil {
		return RaftMembershipView{}, errors.New("raft control not configured")
	}
	future := n.raft.GetConfiguration()
	if err := future.Error(); err != nil {
		return RaftMembershipView{}, fmt.Errorf("read raft configuration: %w", err)
	}
	view := RaftMembershipView{
		Members:   make(map[string]RaftSuffrage, len(future.Configuration().Servers)),
		HasQuorum: n.HasQuorum(),
	}
	for _, server := range future.Configuration().Servers {
		var suffrage RaftSuffrage
		switch server.Suffrage {
		case raft.Voter:
			suffrage = RaftVoter
		case raft.Nonvoter, raft.Staging:
			suffrage = RaftNonVoter
		default:
			return RaftMembershipView{}, fmt.Errorf("unknown raft suffrage %d", server.Suffrage)
		}
		view.Members[string(server.ID)] = suffrage
	}
	if !view.HasQuorum {
		return view, nil
	}
	_, leaderID := n.raft.LeaderWithID()
	if view.Members[string(leaderID)] == RaftVoter {
		view.LeaderID = string(leaderID)
	}
	return view, nil
}

func (n *Node) IsLeader() bool {
	return n != nil && n.raft != nil && n.raft.State() == raft.Leader
}

func (n *Node) IsVoter() bool {
	if n == nil || n.raft == nil {
		return false
	}
	fut := n.raft.GetConfiguration()
	if err := fut.Error(); err != nil {
		return false
	}
	for _, srv := range fut.Configuration().Servers {
		if string(srv.ID) == n.id {
			return srv.Suffrage == raft.Voter
		}
	}
	return false
}

func (n *Node) LeaderAddr() string {
	if n == nil || n.raft == nil {
		return ""
	}
	return string(n.raft.Leader())
}

// CurrentTerm returns the Raft term used to fence control-plane mutations.
func (n *Node) CurrentTerm() uint64 {
	if n == nil || n.raft == nil {
		return 0
	}
	return n.raft.CurrentTerm()
}

func (n *Node) Advertise() string { return n.advertise }

func (n *Node) View() State {
	if n == nil || n.fsm == nil {
		return *NewState()
	}
	return n.fsm.View()
}

func (n *Node) LastContact() time.Time {
	if n == nil || n.raft == nil {
		return time.Time{}
	}
	return n.raft.LastContact()
}

func (n *Node) CacheFresh(ttl time.Duration) bool {
	if n.HasQuorum() {
		return true
	}
	return time.Since(n.LastContact()) < ttl
}

func (n *Node) AddNonvoter(id, addr string) error {
	if err := n.requireLeader(); err != nil {
		return err
	}
	return mapWriteErr(n.raft.AddNonvoter(raft.ServerID(id), raft.ServerAddress(addr), 0, membershipTO).Error())
}

func (n *Node) AddVoter(id, addr string) error {
	if err := n.requireLeader(); err != nil {
		return err
	}
	return mapWriteErr(n.raft.AddVoter(raft.ServerID(id), raft.ServerAddress(addr), 0, membershipTO).Error())
}

func (n *Node) RemoveServer(id string) error {
	if err := n.requireLeader(); err != nil {
		return err
	}
	return mapWriteErr(n.raft.RemoveServer(raft.ServerID(id), 0, membershipTO).Error())
}

func (n *Node) Shutdown() error {
	if n == nil {
		return nil
	}
	n.closeOnce.Do(func() {
		if n.raft != nil {
			n.closeErr = n.raft.Shutdown().Error()
		}
		for _, c := range n.closers {
			if c == nil {
				continue
			}
			if err := c.Close(); n.closeErr == nil {
				n.closeErr = err
			}
		}
	})
	return n.closeErr
}

func (n *Node) requireLeader() error {
	if !n.IsLeader() {
		return notLeader()
	}
	return nil
}

func notLeader() error {
	return errcode.E(errcode.UNAVAILABLE, notLeaderMsg)
}

func mapWriteErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, raft.ErrNotLeader) ||
		errors.Is(err, raft.ErrLeadershipLost) ||
		errors.Is(err, raft.ErrLeadershipTransferInProgress) ||
		errors.Is(err, raft.ErrRaftShutdown) ||
		errors.Is(err, raft.ErrEnqueueTimeout) {
		return notLeader()
	}
	return err
}

type boltSnapshotStore struct {
	db *bbolt.DB
}

var _ raft.SnapshotStore = (*boltSnapshotStore)(nil)

type boltSnapRecord struct {
	Version            raft.SnapshotVersion `json:"version"`
	ID                 string               `json:"id"`
	Index              uint64               `json:"index"`
	Term               uint64               `json:"term"`
	Peers              []byte               `json:"peers"`
	Configuration      []byte               `json:"configuration"`
	ConfigurationIndex uint64               `json:"configuration_index"`
	Size               int64                `json:"size"`
	Data               []byte               `json:"data"`
}

func openBoltSnapshotStore(path string) (*boltSnapshotStore, error) {
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(snapBucket))
		return err
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &boltSnapshotStore{db: db}, nil
}

func (s *boltSnapshotStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *boltSnapshotStore) Create(version raft.SnapshotVersion, index, term uint64, configuration raft.Configuration, configurationIndex uint64, trans raft.Transport) (raft.SnapshotSink, error) {
	if version != 1 {
		return nil, fmt.Errorf("unsupported snapshot version %d", version)
	}
	id := fmt.Sprintf("%d-%d-%d", term, index, time.Now().UnixNano()/int64(time.Millisecond))
	var peers []byte
	if trans != nil {
		peers = raft.EncodeConfiguration(configuration)
	}
	return &boltSnapshotSink{
		store: s,
		rec: boltSnapRecord{
			Version:            version,
			ID:                 id,
			Index:              index,
			Term:               term,
			Peers:              peers,
			Configuration:      raft.EncodeConfiguration(configuration),
			ConfigurationIndex: configurationIndex,
		},
	}, nil
}

func (s *boltSnapshotStore) List() ([]*raft.SnapshotMeta, error) {
	recs, err := s.all()
	if err != nil {
		return nil, err
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Index != recs[j].Index {
			return recs[i].Index > recs[j].Index
		}
		return recs[i].Term > recs[j].Term
	})
	out := make([]*raft.SnapshotMeta, 0, len(recs))
	for i := range recs {
		out = append(out, recs[i].meta())
	}
	return out, nil
}

func (s *boltSnapshotStore) Open(id string) (*raft.SnapshotMeta, io.ReadCloser, error) {
	var rec boltSnapRecord
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(snapBucket))
		if b == nil {
			return fmt.Errorf("snapshot bucket missing")
		}
		raw := b.Get([]byte(id))
		if raw == nil {
			return fmt.Errorf("snapshot %s not found", id)
		}
		return json.Unmarshal(raw, &rec)
	})
	if err != nil {
		return nil, nil, err
	}
	return rec.meta(), io.NopCloser(bytes.NewReader(rec.Data)), nil
}

func (s *boltSnapshotStore) put(rec boltSnapRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(snapBucket))
		if b == nil {
			return fmt.Errorf("snapshot bucket missing")
		}
		if err := b.Put([]byte(rec.ID), raw); err != nil {
			return err
		}
		return reapSnaps(b)
	})
}

func (s *boltSnapshotStore) all() ([]boltSnapRecord, error) {
	var recs []boltSnapRecord
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(snapBucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(_, v []byte) error {
			var rec boltSnapRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			recs = append(recs, rec)
			return nil
		})
	})
	return recs, err
}

func reapSnaps(b *bbolt.Bucket) error {
	var recs []boltSnapRecord
	if err := b.ForEach(func(_, v []byte) error {
		var rec boltSnapRecord
		if err := json.Unmarshal(v, &rec); err != nil {
			return err
		}
		recs = append(recs, rec)
		return nil
	}); err != nil {
		return err
	}
	if len(recs) <= snapRetain {
		return nil
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Index != recs[j].Index {
			return recs[i].Index > recs[j].Index
		}
		return recs[i].Term > recs[j].Term
	})
	for _, rec := range recs[snapRetain:] {
		if err := b.Delete([]byte(rec.ID)); err != nil {
			return err
		}
	}
	return nil
}

func (r boltSnapRecord) meta() *raft.SnapshotMeta {
	var cfg raft.Configuration
	if len(r.Configuration) > 0 {
		cfg = raft.DecodeConfiguration(r.Configuration)
	}
	return &raft.SnapshotMeta{
		Version:            r.Version,
		ID:                 r.ID,
		Index:              r.Index,
		Term:               r.Term,
		Peers:              r.Peers,
		Configuration:      cfg,
		ConfigurationIndex: r.ConfigurationIndex,
		Size:               r.Size,
	}
}

type boltSnapshotSink struct {
	store  *boltSnapshotStore
	rec    boltSnapRecord
	buf    bytes.Buffer
	closed bool
}

func (s *boltSnapshotSink) ID() string { return s.rec.ID }

func (s *boltSnapshotSink) Write(p []byte) (int, error) {
	return s.buf.Write(p)
}

func (s *boltSnapshotSink) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.rec.Data = append([]byte(nil), s.buf.Bytes()...)
	s.rec.Size = int64(len(s.rec.Data))
	return s.store.put(s.rec)
}

func (s *boltSnapshotSink) Cancel() error {
	s.closed = true
	return nil
}
