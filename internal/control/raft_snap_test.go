package control

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestBoltSnapshotStore_CreateListOpen(t *testing.T) {
	s, err := openBoltSnapshotStore(filepath.Join(t.TempDir(), "snapshots.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.Create(0, 1, 1, raft.Configuration{}, 0, nil); err == nil {
		t.Fatal("expected unsupported version")
	}

	cfg := raft.Configuration{Servers: []raft.Server{{
		Suffrage: raft.Voter, ID: "n1", Address: "127.0.0.1:1",
	}}}
	sink, err := s.Create(1, 10, 2, cfg, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sink.ID() == "" {
		t.Fatal("empty snapshot id")
	}
	if _, err := sink.Write([]byte(`{"cluster_id":"c"}`)); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	if list[0].Index != 10 || list[0].Term != 2 || list[0].ConfigurationIndex != 3 {
		t.Fatalf("%+v", list[0])
	}
	if len(list[0].Configuration.Servers) != 1 || list[0].Configuration.Servers[0].ID != "n1" {
		t.Fatalf("cfg=%+v", list[0].Configuration)
	}

	meta, rc, err := s.Open(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if meta.Size != int64(len(data)) || string(data) != `{"cluster_id":"c"}` {
		t.Fatalf("meta=%+v data=%s", meta, data)
	}

	if _, _, err := s.Open("missing"); err == nil {
		t.Fatal("expected missing snapshot error")
	}

	canceled, err := s.Create(1, 11, 2, cfg, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := canceled.Write([]byte("nope")); err != nil {
		t.Fatal(err)
	}
	if err := canceled.Cancel(); err != nil {
		t.Fatal(err)
	}
	list, err = s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("after cancel list=%d err=%v", len(list), err)
	}

	// Newer snapshots first; retain only snapRetain.
	for i := uint64(20); i < 20+snapRetain+1; i++ {
		sk, err := s.Create(1, i, 3, cfg, i, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sk.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := sk.Close(); err != nil {
			t.Fatal(err)
		}
	}
	list, err = s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != snapRetain {
		t.Fatalf("retain=%d got=%d", snapRetain, len(list))
	}
	if list[0].Index < list[len(list)-1].Index {
		t.Fatalf("not descending: %+v", list)
	}

	_, trans := raft.NewInmemTransport("")
	withPeer, err := s.Create(1, 99, 4, cfg, 9, trans)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withPeer.Write([]byte("p")); err != nil {
		t.Fatal(err)
	}
	if err := withPeer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRaft_StartErrorsAndNilNode(t *testing.T) {
	if _, err := Start(RaftConfig{Dir: t.TempDir(), Bind: "not-a-bind", NodeID: "n"}); err == nil {
		t.Fatal("expected bind error")
	}
	if _, err := Start(RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: "n", Advertise: "%%%"}); err == nil {
		t.Fatal("expected advertise error")
	}
	if _, err := Start(RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0"}); err == nil {
		t.Fatal("expected empty node id error")
	}
	file := filepath.Join(t.TempDir(), "notdir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(RaftConfig{Dir: file, Bind: "127.0.0.1:0", NodeID: "n"}); err == nil {
		t.Fatal("expected dir error")
	}

	var n *Node
	if n.HasQuorum() || n.IsLeader() || n.LeaderAddr() != "" {
		t.Fatal("nil node")
	}
	_ = n.View()
	_ = n.LastContact()
	if err := n.Shutdown(); err != nil {
		t.Fatal(err)
	}

	if err := mapWriteErr(nil); err != nil {
		t.Fatal(err)
	}
	for _, e := range []error{raft.ErrNotLeader, raft.ErrLeadershipLost, raft.ErrLeadershipTransferInProgress, raft.ErrRaftShutdown, raft.ErrEnqueueTimeout} {
		if !errcode.Is(mapWriteErr(e), errcode.UNAVAILABLE) {
			t.Fatalf("map %v", e)
		}
	}
	if !errors.Is(mapWriteErr(io.ErrUnexpectedEOF), io.ErrUnexpectedEOF) {
		t.Fatal("passthrough")
	}

	n2, err := Start(RaftConfig{Dir: t.TempDir(), Bind: "127.0.0.1:0", NodeID: "n1", Advertise: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if n2.Advertise() != "127.0.0.1:0" {
		t.Fatalf("advertise=%q", n2.Advertise())
	}
	if err := n2.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := n2.Shutdown(); err != nil {
		t.Fatal(err)
	}

	_, trans := raft.NewInmemTransport("")
	inmem, err := StartInmem("x", nil, trans)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inmem.Shutdown() })
}

func TestRaft_ForcedSnapshot(t *testing.T) {
	dir := t.TempDir()
	n, err := Start(RaftConfig{Dir: dir, Bind: "127.0.0.1:0", NodeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	if err := n.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !n.IsLeader() || !n.HasQuorum() {
		if time.Now().After(deadline) {
			t.Fatal("no leader")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cmd, err := EncodeCommand(CmdBootstrap, BootstrapBody{
		ClusterID: "c", AdminUser: "admin", PasswordHash: "h", AdminUserID: "u", NowUnix: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Apply(cmd, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := n.raft.Snapshot().Error(); err != nil {
		t.Fatal(err)
	}
}
