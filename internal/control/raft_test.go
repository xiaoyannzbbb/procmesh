package control_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestRaft_BootstrapApplyVisible(t *testing.T) {
	dir := t.TempDir()
	n, err := control.Start(control.RaftConfig{
		Dir:       dir,
		Bind:      "127.0.0.1:0",
		NodeID:    "n1",
		ClusterID: "cid-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })

	if _, err := os.Stat(filepath.Join(dir, "raft.db")); err != nil {
		t.Fatalf("raft.db: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshots.bolt")); err != nil {
		t.Fatalf("snapshots.bolt: %v", err)
	}

	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLeader(t, []*control.Node{n}, 10*time.Second)

	cmd := mustEncode(t, control.CmdBootstrap, control.BootstrapBody{
		ClusterID:    "cid-1",
		AdminUser:    "admin",
		PasswordHash: "hashed-admin",
		AdminUserID:  "user-admin",
		NowUnix:      time.Now().Unix(),
	})
	if err := n.Apply(cmd, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	view := n.View()
	admin, ok := view.Users["admin"]
	if !ok || admin.ID != "user-admin" || admin.Status != control.UserActive {
		t.Fatalf("admin=%+v ok=%v", admin, ok)
	}
	if view.ClusterID != "cid-1" {
		t.Fatalf("cluster=%q", view.ClusterID)
	}
	err = n.Apply(cmd, 5*time.Second)
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("rebootstrap: %v", err)
	}
	if n.Advertise() == "" {
		t.Fatal("empty advertise")
	}
	if !n.IsLeader() {
		t.Fatal("expected leader")
	}
	if n.LeaderAddr() == "" {
		t.Fatal("empty leader addr")
	}
}

func TestRaft_ClaimBackupFireExistingIsNotCreated(t *testing.T) {
	_, trans := raft.NewInmemTransport("")
	n, err := control.StartInmem("solo-fire", control.NewFSM(), trans)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLeader(t, []*control.Node{n}, 10*time.Second)
	now := time.Now().Truncate(time.Second)
	first, created, err := n.ClaimBackupFire(control.FireClaimBody{OperationID: "op-fire-first", FireKey: "bp:1700000000", PolicyID: "bp", LeaderTerm: 1, ScheduledUnix: now.Unix(), LeaseUntilUnix: now.Add(30 * time.Second).Unix()}, now)
	if err != nil || !created {
		t.Fatalf("first=%+v created=%v err=%v", first, created, err)
	}
	existing, created, err := n.ClaimBackupFire(control.FireClaimBody{OperationID: "op-fire-existing", FireKey: first.FireKey, PolicyID: first.PolicyID, LeaderTerm: 1, ScheduledUnix: first.ScheduledUnix}, now)
	if err != nil || created || existing.RunID != first.RunID || existing.LeaderTerm != 1 {
		t.Fatalf("existing=%+v created=%v err=%v", existing, created, err)
	}
}

func TestRaft_ThreeNodeLoseQuorumRejectsWrite(t *testing.T) {
	nodes := startInmemVoters(t, 3)
	leader := waitLeader(t, nodes, 10*time.Second)

	cmd := mustEncode(t, control.CmdUserPut, control.UserPutBody{
		ID: "u1", Username: "alice", PasswordHash: "h1",
	})
	if err := leader.Apply(cmd, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, ok := leader.View().Users["alice"]; !ok {
		t.Fatal("alice missing after apply")
	}

	var remain *control.Node
	for _, n := range nodes {
		if n.IsLeader() {
			remain = n
			continue
		}
		if err := n.Shutdown(); err != nil {
			t.Fatal(err)
		}
	}
	if remain == nil {
		t.Fatal("no remaining node")
	}

	deadline := time.Now().Add(10 * time.Second)
	for remain.HasQuorum() {
		if time.Now().After(deadline) {
			t.Fatal("remaining node still reports quorum")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if remain.HasQuorum() {
		t.Fatal("HasQuorum()==true after losing two voters")
	}

	cmd2 := mustEncode(t, control.CmdUserPut, control.UserPutBody{
		ID: "u2", Username: "bob", PasswordHash: "h2",
	})
	err := remain.Apply(cmd2, 5*time.Second)
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("second apply: %v", err)
	}
	view := remain.View()
	if _, ok := view.Users["alice"]; !ok {
		t.Fatal("alice missing from cached view")
	}
	if _, ok := view.Users["bob"]; ok {
		t.Fatal("bob should not have been applied")
	}
}

func TestRaft_FollowerApplyRejected(t *testing.T) {
	nodes := startInmemVoters(t, 3)
	leader := waitLeader(t, nodes, 10*time.Second)

	var follower *control.Node
	for _, n := range nodes {
		if n != leader && !n.IsLeader() {
			follower = n
			break
		}
	}
	if follower == nil {
		t.Fatal("no follower")
	}

	cmd := mustEncode(t, control.CmdUserPut, control.UserPutBody{
		ID: "u1", Username: "carol", PasswordHash: "h",
	})
	err := follower.Apply(cmd, time.Second)
	if !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("apply: %v", err)
	}
	if err == nil || err.Error() != "UNAVAILABLE: not raft leader" {
		t.Fatalf("msg=%v", err)
	}
	if err := follower.AddVoter("x", "addr"); !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("add voter: %v", err)
	}
	if err := follower.AddNonvoter("x", "addr"); !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("add nonvoter: %v", err)
	}
	if err := follower.RemoveServer("x"); !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("remove: %v", err)
	}
}

func TestRaft_HasQuorumFalseAfterVoterDown(t *testing.T) {
	addr0, trans0 := raft.NewInmemTransport("")
	addr1, trans1 := raft.NewInmemTransport("")
	trans0.Connect(addr1, trans1)
	trans1.Connect(addr0, trans0)

	voter, err := control.StartInmem("voter", control.NewFSM(), trans0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = voter.Shutdown() })
	nv, err := control.StartInmem("nv-1", control.NewFSM(), trans1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nv.Shutdown() })

	if err := voter.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLeader(t, []*control.Node{voter}, 10*time.Second)
	if err := voter.AddNonvoter("nv-1", string(addr1)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for !nv.HasQuorum() || nv.LeaderAddr() == "" {
		if time.Now().After(deadline) {
			t.Fatalf("nonvoter never saw quorum leader=%q has=%v", nv.LeaderAddr(), nv.HasQuorum())
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := voter.Shutdown(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for nv.HasQuorum() {
		if time.Now().After(deadline) {
			t.Fatal("HasQuorum()==true after voter down")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if nv.HasQuorum() {
		t.Fatal("remaining nonvoter must not report quorum")
	}
}

func TestRaft_CacheFreshAfterPartition(t *testing.T) {
	dir := t.TempDir()
	n, err := control.Start(control.RaftConfig{
		Dir:    dir,
		Bind:   "127.0.0.1:0",
		NodeID: "n1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLeader(t, []*control.Node{n}, 10*time.Second)
	if !n.CacheFresh(5 * time.Minute) {
		t.Fatal("expected cache fresh after bootstrap")
	}
	if !n.HasQuorum() {
		t.Fatal("single voter should have quorum")
	}
	_ = n.LastContact()
}

func TestRaft_CacheFreshWithoutLeader(t *testing.T) {
	_, trans := raft.NewInmemTransport("")
	n, err := control.StartInmem("solo", control.NewFSM(), trans)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if n.HasQuorum() {
		t.Fatal("unbootstrapped node has quorum")
	}
	if n.CacheFresh(time.Millisecond) {
		t.Fatal("expected stale cache")
	}
	_ = n.LastContact()
}

func TestRaft_RestartReplaysLog(t *testing.T) {
	dir := t.TempDir()
	cfg := control.RaftConfig{Dir: dir, Bind: "127.0.0.1:0", NodeID: "n1", ClusterID: "cid"}
	n, err := control.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLeader(t, []*control.Node{n}, 10*time.Second)
	cmd := mustEncode(t, control.CmdBootstrap, control.BootstrapBody{
		ClusterID: "cid", AdminUser: "admin", PasswordHash: "h", AdminUserID: "ua", NowUnix: 1,
	})
	if err := n.Apply(cmd, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := n.Shutdown(); err != nil {
		t.Fatal(err)
	}

	n2, err := control.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n2.Shutdown() })
	deadline := time.Now().Add(10 * time.Second)
	for n2.View().Users["admin"].ID == "" {
		if time.Now().After(deadline) {
			t.Fatalf("view=%+v", n2.View())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRaft_MembershipAddRemove(t *testing.T) {
	nodes := startInmemVoters(t, 1)
	leader := waitLeader(t, nodes, 10*time.Second)

	addr, trans := raft.NewInmemTransport("")
	// Isolated nonvoter: start without connecting so AddNonvoter still records membership.
	nv, err := control.StartInmem("nv-1", control.NewFSM(), trans)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nv.Shutdown() })
	if err := leader.AddNonvoter("nv-1", string(addr)); err != nil {
		t.Fatal(err)
	}
	if err := leader.RemoveServer("nv-1"); err != nil {
		t.Fatal(err)
	}
}

func TestNode_IsVoter(t *testing.T) {
	nodes := startInmemVoters(t, 3)
	waitLeader(t, nodes, 10*time.Second)
	for i, n := range nodes {
		if !n.IsVoter() {
			t.Fatalf("voter node-%d IsVoter=false", i+1)
		}
	}

	addr0, trans0 := raft.NewInmemTransport("")
	addr1, trans1 := raft.NewInmemTransport("")
	trans0.Connect(addr1, trans1)
	trans1.Connect(addr0, trans0)

	voter, err := control.StartInmem("voter", control.NewFSM(), trans0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = voter.Shutdown() })
	nv, err := control.StartInmem("nv-1", control.NewFSM(), trans1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nv.Shutdown() })

	if err := voter.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	waitLeader(t, []*control.Node{voter}, 10*time.Second)
	if err := voter.AddNonvoter("nv-1", string(addr1)); err != nil {
		t.Fatal(err)
	}
	if !voter.IsVoter() {
		t.Fatal("bootstrap voter IsVoter=false")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if nv.LeaderAddr() != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if nv.IsVoter() {
		t.Fatal("nonvoter IsVoter=true")
	}
}

func startInmemVoters(t *testing.T, n int) []*control.Node {
	t.Helper()
	type spec struct {
		id    string
		addr  raft.ServerAddress
		trans *raft.InmemTransport
	}
	specs := make([]spec, n)
	for i := 0; i < n; i++ {
		addr, trans := raft.NewInmemTransport("")
		specs[i] = spec{id: fmt.Sprintf("node-%d", i+1), addr: addr, trans: trans}
	}
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			specs[i].trans.Connect(specs[j].addr, specs[j].trans)
		}
	}
	nodes := make([]*control.Node, n)
	for i := 0; i < n; i++ {
		node, err := control.StartInmem(specs[i].id, control.NewFSM(), specs[i].trans)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = node.Shutdown() })
		nodes[i] = node
	}
	if err := nodes[0].Bootstrap(); err != nil {
		t.Fatal(err)
	}
	leader := waitLeader(t, nodes[:1], 10*time.Second)
	for i := 1; i < n; i++ {
		if err := leader.AddVoter(specs[i].id, string(specs[i].addr)); err != nil {
			t.Fatalf("add voter %s: %v", specs[i].id, err)
		}
	}
	if n > 1 {
		waitLeader(t, nodes, 10*time.Second)
	}
	waitVoters(t, nodes, 10*time.Second)
	return nodes
}

func waitVoters(t *testing.T, nodes []*control.Node, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		ready := true
		for _, n := range nodes {
			if !n.IsVoter() {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	missing := make([]int, 0, len(nodes))
	for i, n := range nodes {
		if !n.IsVoter() {
			missing = append(missing, i+1)
		}
	}
	t.Fatalf("timeout waiting for voters; not voters: %v", missing)
}

func waitLeader(t *testing.T, nodes []*control.Node, d time.Duration) *control.Node {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var leaders []*control.Node
		for _, n := range nodes {
			if n.IsLeader() {
				leaders = append(leaders, n)
			}
		}
		if len(leaders) == 1 && leaders[0].HasQuorum() {
			return leaders[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no leader")
	return nil
}
