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
	"github.com/qleelulu/procmesh/internal/control"
)

const defaultControlListen = "127.0.0.1:9002"

const (
	raftStartTO   = 10 * time.Second
	raftApplyTO   = 5 * time.Second
	adminUserID   = "user-admin"
	raftPollEvery = 20 * time.Millisecond
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
	adv = advertise
	if adv == "" {
		adv = bind
		return bind, adv, nil
	}
	if _, port, splitErr := net.SplitHostPort(adv); splitErr == nil && port == "0" {
		_, bindPort, _ := net.SplitHostPort(bind)
		host, _, _ := net.SplitHostPort(adv)
		adv = net.JoinHostPort(host, bindPort)
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
	bootstrap := false
	if meta, err := control.LoadMeta(r.dir); err == nil && meta.ControlMember {
		bootstrap = !raftLogExists(r.raftDir)
	}
	if err := r.startRaft(bootstrap); err != nil {
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

func (r *rpcRuntime) startRaft(bootstrap bool) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.node == nil {
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
		if r.auth != nil {
			r.auth.Store = n
		}
		adv := n.Advertise()
		onListen := r.opt.OnControlListen
		r.mu.Unlock()
		if onListen != nil {
			onListen(adv)
		}
	} else {
		r.mu.Unlock()
	}
	if !bootstrap {
		return nil
	}
	return r.bootstrapFSM()
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
		if n.View().ClusterID != "" {
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
	n := r.control()
	if n == nil {
		return ""
	}
	localAPI := ""
	if r.src != nil {
		localAPI = r.src.Snapshot().APIAddress
	}
	if n.IsLeader() {
		return localAPI
	}
	leaderRaft := n.LeaderAddr()
	if leaderRaft == "" {
		r.mu.Lock()
		leaderRaft = r.knownLeader
		r.mu.Unlock()
	}
	if leaderRaft == "" || leaderRaft == n.Advertise() {
		return localAPI
	}
	view := n.View()
	if r.mesh == nil {
		return ""
	}
	for id, m := range view.Members {
		if m.RaftAddr != leaderRaft {
			continue
		}
		for _, mem := range r.mesh.Members() {
			if mem.NodeID == id && mem.APIAddress != "" {
				return mem.APIAddress
			}
		}
	}
	return ""
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
