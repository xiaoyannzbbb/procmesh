package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

const defaultControlListen = "127.0.0.1:18685"

const (
	raftStartTO              = 10 * time.Second
	raftApplyTO              = 5 * time.Second
	adminUserID              = "user-admin"
	raftPollEvery            = 20 * time.Millisecond
	membershipReconcileEvery = 5 * time.Second
)

func resolveControlAddr(listen, advertise string) (bind, adv string, err error) {
	bind = listen
	if bind == "" {
		bind = defaultControlListen
	}
	if _, port, splitErr := net.SplitHostPort(bind); splitErr == nil && port == "0" {
		ln, lerr := net.Listen("tcp", bind)
		if lerr != nil {
			return "", "", lerr
		}
		bind = ln.Addr().String()
		_ = ln.Close()
	}
	if advertise == "" {
		adv = bind
		return bind, adv, nil
	}
	adv, err = resolveAdvertiseAddr(bind, advertise)
	if err != nil {
		return "", "", err
	}
	return bind, adv, nil
}

func raftLogExists(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "raft.db"))
	return err == nil && !st.IsDir()
}

func (r *rpcRuntime) control() *control.Node {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.node
}

func (r *rpcRuntime) setKnownLeader(addr string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.knownLeader = addr
	r.mu.Unlock()
}

func (r *rpcRuntime) raftAddr() string {
	if n := r.control(); n != nil {
		if a := n.Advertise(); a != "" {
			return a
		}
	}
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.controlAdv != "" {
		return r.controlAdv
	}
	return r.controlBind
}

func (r *rpcRuntime) onReady() error {
	if r == nil {
		return nil
	}
	if err := r.startControl(); err != nil {
		return err
	}
	if err := r.startRPC(); err != nil {
		return err
	}
	if meta, err := control.LoadMeta(r.dir); err == nil && !meta.ControlMember {
		return r.waitCatchup(raftStartTO)
	}
	return nil
}

func (r *rpcRuntime) startControl() error {
	if r == nil {
		return nil
	}
	bootstrap := false
	if meta, err := control.LoadMeta(r.dir); err == nil && meta.ControlMember {
		bootstrap = !raftLogExists(r.raftDir)
	}
	return r.startRaft(bootstrap)
}

func (r *rpcRuntime) startRaft(bootstrap bool) (retErr error) {
	if r == nil {
		return nil
	}
	recovering := raftLogExists(r.raftDir)
	var started *control.Node
	freshStart := false
	defer func() {
		if retErr == nil || !freshStart {
			return
		}
		retErr = errors.Join(retErr, r.rollbackFreshRaft(started))
	}()
	r.mu.Lock()
	if r.node == nil {
		freshStart = !recovering
		clusterID := r.clusterID
		if clusterID == "" {
			clusterID = r.lookupClusterIDLocked()
		}
		bind := r.controlBind
		if bind == "" {
			bind = defaultControlListen
		}
		n, err := control.Start(control.RaftConfig{
			Dir:       r.raftDir,
			Bind:      bind,
			Advertise: r.controlAdv,
			NodeID:    r.nodeID,
			ClusterID: clusterID,
		})
		if err != nil {
			r.mu.Unlock()
			return err
		}
		r.node = n
		started = n
		if r.auth != nil {
			r.auth.SetStore(n)
		}
		adv := n.Advertise()
		onListen := r.opt.OnControlListen
		r.mu.Unlock()
		if onListen != nil {
			onListen(adv)
		}
		r.logger.With("component", "raft").Info("raft control listening", "address", adv)
	} else {
		r.mu.Unlock()
	}
	if bootstrap {
		if err := r.bootstrapFSM(); err != nil {
			return err
		}
	} else if recovering {
		if err := r.waitCatchup(raftStartTO); err != nil && r.logger != nil {
			r.logger.With("component", "raft").Warn("raft fsm not caught up", "error", err)
		}
	}
	if err := r.ensureLocalMemberAdmitted(); err != nil {
		return err
	}
	r.startMembershipReconciler()
	return nil
}

func (r *rpcRuntime) rollbackFreshRaft(started *control.Node) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if started != nil && r.node == started {
		r.node = nil
	}
	r.mu.Unlock()
	if started != nil && r.auth != nil {
		r.auth.SetStore(nil)
	}
	var rollbackErr error
	if started != nil {
		rollbackErr = started.Shutdown()
	}
	if r.raftDir != "" {
		if err := os.RemoveAll(r.raftDir); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove fresh raft state: %w", err))
		}
	}
	return rollbackErr
}

func (r *rpcRuntime) startMembershipReconciler() {
	if r == nil || r.ctx == nil {
		return
	}
	r.membershipOnce.Do(func() {
		r.reconcileRaftMembership()
		go func() {
			ticker := time.NewTicker(membershipReconcileEvery)
			defer ticker.Stop()
			for {
				select {
				case <-r.ctx.Done():
					return
				case <-ticker.C:
					r.reconcileRaftMembership()
				}
			}
		}()
	})
}

func (r *rpcRuntime) reconcileRaftMembership() {
	n := r.control()
	if n == nil || !n.IsLeader() {
		return
	}
	if err := n.ReconcileRaftMembership(); err != nil && r.logger != nil {
		r.logger.With("component", "raft").Warn("raft membership reconcile failed", "error", err)
	}
}

func (r *rpcRuntime) ensureLocalMemberAdmitted() error {
	if r == nil {
		return nil
	}
	n := r.control()
	if n == nil || r.dir == "" {
		return nil
	}
	meta, err := control.LoadMeta(r.dir)
	if err != nil || !meta.ControlMember || meta.NodeID == "" {
		return nil
	}
	view := n.View()
	if view.ClusterID == "" {
		return nil
	}
	if m, ok := view.Member(meta.NodeID); !ok || m.Status != control.MemberAdmitted {
		if !(n.IsLeader() && n.HasQuorum()) {
			if err := waitRaftLeader(n, raftStartTO); err != nil {
				return nil
			}
		}
		if err := admitBootstrapMember(n, r.dir); err != nil {
			return err
		}
	}
	if n.IsLeader() {
		if err := (control.CapabilityManager{Node: n, Dir: r.dir, NodeID: meta.NodeID}).EnsureInitialized(); err != nil && r.logger != nil {
			r.logger.With("component", "raft").Warn("admission capability initialization deferred")
		}
	}
	return nil
}

func (r *rpcRuntime) lookupClusterIDLocked() string {
	if r.st != nil {
		if id, err := r.st.GetClusterID(context.Background()); err == nil && id != "" {
			return id
		}
	}
	if r.dir != "" {
		if meta, err := control.LoadMeta(r.dir); err == nil {
			return meta.ClusterID
		}
	}
	return ""
}

func (r *rpcRuntime) bootstrapFSM() error {
	n := r.control()
	if n == nil {
		return fmt.Errorf("raft not started")
	}
	if n.View().ClusterID != "" {
		return nil
	}
	if err := n.Bootstrap(); err != nil && !isAlreadyBootstrapped(err) {
		return err
	}
	if err := waitRaftLeader(n, raftStartTO); err != nil {
		return err
	}
	return applyAdminBootstrap(n, r.dir)
}

func (r *rpcRuntime) waitCatchup(d time.Duration) error {
	n := r.control()
	if n == nil {
		return fmt.Errorf("raft not started")
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if n.View().ClusterID != "" && (n.IsLeader() || n.LeaderAddr() != "") {
			return nil
		}
		time.Sleep(raftPollEvery)
	}
	return fmt.Errorf("raft fsm not caught up")
}

func (r *rpcRuntime) onAdmit(nodeID, raftAddr string) error {
	n := r.control()
	if n == nil || raftAddr == "" {
		return nil
	}
	if raftAddr == n.Advertise() {
		return nil
	}
	return n.AddNonvoter(nodeID, raftAddr)
}

func (r *rpcRuntime) leaderAPI() string {
	if r.control() == nil {
		return ""
	}
	localAPI := ""
	if r.src != nil {
		localAPI = r.src.Snapshot().APIAddress
	}
	leaderID, ok := r.leaderNodeID()
	if !ok || leaderID == r.nodeID {
		return localAPI
	}
	if r.mesh == nil {
		return ""
	}
	for _, mem := range r.mesh.Members() {
		if mem.NodeID == leaderID && mem.APIAddress != "" {
			return mem.APIAddress
		}
	}
	return ""
}

func (r *rpcRuntime) leaderNodeID() (string, bool) {
	n := r.control()
	if n == nil {
		return "", false
	}
	if n.IsLeader() {
		return r.nodeID, true
	}
	leaderRaft := n.LeaderAddr()
	if leaderRaft == "" {
		r.mu.Lock()
		leaderRaft = r.knownLeader
		r.mu.Unlock()
	}
	if leaderRaft == "" {
		return "", false
	}
	if leaderRaft == n.Advertise() {
		return r.nodeID, true
	}
	for id, member := range n.View().Members {
		if member.RaftAddr == leaderRaft && member.Status == control.MemberAdmitted {
			return id, true
		}
	}
	return "", false
}

func (r *rpcRuntime) leaderRoute() (api.Route, error) {
	n := r.control()
	if n == nil {
		return api.Route{}, errcode.E(errcode.UNAVAILABLE, "raft control unavailable")
	}
	leaderID, ok := r.leaderNodeID()
	if !ok {
		return api.Route{}, errcode.E(errcode.UNAVAILABLE, "raft leader unavailable")
	}
	if leaderID == r.nodeID {
		return api.Route{Local: true, NodeID: r.nodeID}, nil
	}
	if r.mesh == nil {
		return api.Route{}, errcode.E(errcode.UNAVAILABLE, "gossip unavailable")
	}
	view := n.View()
	router := api.Router{
		LocalID: r.nodeID,
		Members: r.mesh.Members,
		ControlStatus: func(nodeID string) (string, bool) {
			member, ok := view.Member(nodeID)
			return string(member.Status), ok
		},
	}
	return router.Resolve(context.Background(), leaderID, "", "")
}

func applyAdminBootstrap(n *control.Node, clusterDir string) error {
	if n.View().ClusterID != "" {
		return nil
	}
	user, hash, err := control.LoadAdminBootstrap(clusterDir)
	if err != nil {
		return err
	}
	meta, err := control.LoadMeta(clusterDir)
	if err != nil {
		return err
	}
	cmd, err := control.EncodeCommand(control.CmdBootstrap, control.BootstrapBody{
		ClusterID:    meta.ClusterID,
		AdminUser:    user,
		PasswordHash: hash,
		AdminUserID:  adminUserID,
		NowUnix:      time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	if err := n.Apply(cmd, raftApplyTO); err != nil {
		return err
	}
	if err := admitBootstrapMember(n, clusterDir); err != nil {
		return err
	}
	return (control.CapabilityManager{Node: n, Dir: clusterDir, NodeID: meta.NodeID}).EnsureInitialized()
}

func admitBootstrapMember(n *control.Node, clusterDir string) error {
	meta, err := control.LoadMeta(clusterDir)
	if err != nil {
		return err
	}
	if meta.NodeID == "" {
		return nil
	}
	view := n.View()
	if m, ok := view.Member(meta.NodeID); ok && m.Status == control.MemberAdmitted {
		return nil
	}
	serial := ""
	if b, err := control.LoadBundle(clusterDir); err == nil {
		if s, err := control.CertSerial(b.AgentCertPEM); err == nil {
			serial = s
		}
	}
	cmd, err := control.EncodeCommand(control.CmdMemberPut, control.MemberPutBody{
		NodeID:     meta.NodeID,
		RaftAddr:   n.Advertise(),
		CertSerial: serial,
		Status:     control.MemberAdmitted,
	})
	if err != nil {
		return err
	}
	return n.Apply(cmd, raftApplyTO)
}

func waitRaftLeader(n *control.Node, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if n.IsLeader() && n.HasQuorum() {
			return nil
		}
		time.Sleep(raftPollEvery)
	}
	return fmt.Errorf("raft leader not elected")
}

func isAlreadyBootstrapped(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, raft.ErrCantBootstrap) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "already")
}
