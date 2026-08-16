package batch_test

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/qleelulu/procmesh/internal/batch"
	"github.com/qleelulu/procmesh/internal/errcode"
)

type memCluster struct{ nodes []batch.NodeView }

func (m memCluster) Nodes() []batch.NodeView { return m.nodes }

type memGroups map[string][]string

func (m memGroups) Members(id string) ([]string, error) {
	v, ok := m[id]
	if !ok {
		return nil, errcode.E(errcode.INVALID, "agent group")
	}
	return v, nil
}

type memSpecs map[string]batch.OwnerSpec // key nodeID+"/"+idOrName

func (m memSpecs) Get(_ context.Context, node, id string) (batch.OwnerSpec, error) {
	s, ok := m[node+"/"+id]
	if !ok {
		return batch.OwnerSpec{}, errcode.E(errcode.NOT_FOUND, "process")
	}
	return s, nil
}

type denyAuth struct{}

func (denyAuth) Allow(_, _, _ string) error { return errcode.E(errcode.DENIED, "permission denied") }

type allowAuth struct{}

func (allowAuth) Allow(_, _, _ string) error { return nil }

type recAuth struct {
	node, group, perm string
}

func (r *recAuth) Allow(node, group, perm string) error {
	r.node, r.group, r.perm = node, group, perm
	return nil
}

type denyGroup string

func (d denyGroup) Allow(_, group, _ string) error {
	if group == string(d) {
		return errcode.E(errcode.DENIED, "permission denied")
	}
	return nil
}

func TestExpand_ProcessGroupVerifiesOwnerAndKeepsMismatch(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{
			NodeID: "n1",
			Processes: []batch.ProcView{
				{ProcessID: "p-pay", Name: "pay", Group: "finance"},
				{ProcessID: "p-stale", Name: "ad", Group: "finance"}, // gossip 过期
			},
		}}},
		Specs: memSpecs{
			"n1/p-pay":   {ProcessID: "p-pay", Name: "pay", NodeID: "n1", Group: "finance"},
			"n1/p-stale": {ProcessID: "p-stale", Name: "ad", NodeID: "n1", Group: "ads"},
		},
		Auth: allowAuth{},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{ProcessGroup: "finance"}, batch.TypeRestart)
	if err != nil || len(ts) != 2 {
		t.Fatalf("%+v %v", ts, err)
	}
	var pay, stale batch.Target
	for _, t0 := range ts {
		if t0.ProcessID == "p-pay" {
			pay = t0
		}
		if t0.ProcessID == "p-stale" {
			stale = t0
		}
	}
	if pay.Status != "" && pay.Status != batch.TargetPending {
		t.Fatalf("pay %s", pay.Status)
	}
	if stale.Status != batch.TargetInvalid {
		t.Fatalf("mismatch must be INVALID, got %s", stale.Status)
	}
}

func TestExpand_DeniedKept(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{NodeID: "n1", Processes: []batch.ProcView{{ProcessID: "p1", Name: "x"}}}}},
		Specs:   memSpecs{"n1/p1": {ProcessID: "p1", Name: "x", NodeID: "n1"}},
		Auth:    denyAuth{},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{ProcessIDs: []string{"p1"}}, batch.TypeStart)
	if err != nil || len(ts) != 1 || ts[0].Status != batch.TargetDenied {
		t.Fatalf("%+v %v", ts, err)
	}
}

func TestExpand_IllegalProcessGroupName(t *testing.T) {
	x := &batch.RealExpander{Cluster: memCluster{}, Specs: memSpecs{}, Auth: allowAuth{}}
	ts, err := x.Expand(context.Background(), batch.Selector{ProcessGroup: "has space"}, batch.TypeRestart)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID, got %v %+v", err, ts)
	}
}

func TestExpand_UnknownAgentGroup(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{},
		Groups:  memGroups{"g-other": []string{"n1"}},
		Specs:   memSpecs{},
		Auth:    allowAuth{},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{AgentGroupID: "g-missing"}, batch.TypeStop)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID, got %v %+v", err, ts)
	}
}

func TestExpand_ProcessIDsGossipMissOwnerWins(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{NodeID: "n1"}}},
		Specs:   memSpecs{"n1/p-hidden": {ProcessID: "p-hidden", Name: "hid", NodeID: "n1", Group: "ops"}},
		Auth:    allowAuth{},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{ProcessIDs: []string{"p-hidden"}}, batch.TypeStart)
	if err != nil || len(ts) != 1 || ts[0].ProcessID != "p-hidden" || ts[0].NodeID != "n1" {
		t.Fatalf("%+v %v", ts, err)
	}
	if ts[0].Status != "" && ts[0].Status != batch.TargetPending {
		t.Fatalf("status %s", ts[0].Status)
	}
}

func TestExpand_ProcessIDsNotFoundKept(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{NodeID: "n1", Processes: []batch.ProcView{{ProcessID: "p-gone", Name: "gone"}}}}},
		Specs:   memSpecs{},
		Auth:    allowAuth{},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{ProcessIDs: []string{"p-gone"}}, batch.TypeStop)
	if err != nil || len(ts) != 1 || ts[0].Status != batch.TargetInvalid || ts[0].ProcessID != "p-gone" {
		t.Fatalf("%+v %v", ts, err)
	}
}

func TestExpand_ProcessNames(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{NodeID: "n2"}}},
		Specs:   memSpecs{"n2/nginx": {ProcessID: "p-ngx", Name: "nginx", NodeID: "n2"}},
		Auth:    allowAuth{},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{ProcessNames: []batch.ProcessNameRef{{NodeID: "n2", ProcessName: "nginx"}}}, batch.TypeRestart)
	if err != nil || len(ts) != 1 || ts[0].ProcessID != "p-ngx" || ts[0].ProcessName != "nginx" {
		t.Fatalf("%+v %v", ts, err)
	}
	ts, err = x.Expand(context.Background(), batch.Selector{ProcessNames: []batch.ProcessNameRef{{NodeID: "n2", ProcessName: "missing"}}}, batch.TypeRestart)
	if err != nil || len(ts) != 1 || ts[0].Status != batch.TargetInvalid || ts[0].ProcessName != "missing" {
		t.Fatalf("missing name: %+v %v", ts, err)
	}
}

func TestExpand_AgentGroupSnapshotsMembers(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{
			{NodeID: "n1", Processes: []batch.ProcView{{ProcessID: "p1", Name: "a"}, {ProcessID: "p-gone", Name: "z"}}},
			{NodeID: "n2", Processes: []batch.ProcView{{ProcessID: "p2", Name: "b"}}},
			{NodeID: "n3", Processes: []batch.ProcView{{ProcessID: "p3", Name: "c"}}},
		}},
		Groups: memGroups{"g1": []string{"n1", "n2"}},
		Specs: memSpecs{
			"n1/p1": {ProcessID: "p1", Name: "a", NodeID: "n1"},
			"n2/p2": {ProcessID: "p2", Name: "b", NodeID: "n2"},
			"n3/p3": {ProcessID: "p3", Name: "c", NodeID: "n3"},
		},
		Auth: allowAuth{},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{AgentGroupID: "g1"}, batch.TypeStart)
	if err != nil || len(ts) != 2 {
		t.Fatalf("%+v %v", ts, err)
	}
	got := map[string]bool{}
	for _, t0 := range ts {
		got[t0.ProcessID] = true
		if t0.Status != "" && t0.Status != batch.TargetPending {
			t.Fatalf("status %s", t0.Status)
		}
	}
	if !got["p1"] || !got["p2"] || got["p3"] || got["p-gone"] {
		t.Fatalf("members snapshot: %+v", ts)
	}
}

func TestExpand_DedupesNodeAndProcess(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{
			NodeID:    "n1",
			Processes: []batch.ProcView{{ProcessID: "p1", Name: "x", Group: "g"}},
		}}},
		Specs: memSpecs{
			"n1/p1": {ProcessID: "p1", Name: "x", NodeID: "n1", Group: "g"},
			"n1/x":  {ProcessID: "p1", Name: "x", NodeID: "n1", Group: "g"},
		},
		Auth: allowAuth{},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{
		ProcessIDs:   []string{"p1"},
		ProcessNames: []batch.ProcessNameRef{{NodeID: "n1", ProcessName: "x"}},
		ProcessGroup: "g",
	}, batch.TypeRestart)
	if err != nil || len(ts) != 1 || ts[0].ProcessID != "p1" {
		t.Fatalf("%+v %v", ts, err)
	}
}

func TestExpand_PermByType(t *testing.T) {
	auth := &recAuth{}
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{NodeID: "n1", Processes: []batch.ProcView{{ProcessID: "p1"}}}}},
		Specs:   memSpecs{"n1/p1": {ProcessID: "p1", Name: "x", NodeID: "n1", Group: "fin"}},
		Auth:    auth,
	}
	if _, err := x.Expand(context.Background(), batch.Selector{ProcessIDs: []string{"p1"}}, batch.TypeConfigUpdate); err != nil {
		t.Fatal(err)
	}
	if auth.perm != "process.config.update" || auth.node != "n1" || auth.group != "fin" {
		t.Fatalf("allow %+v", auth)
	}
}

func TestExpand_ConfigOverlay(t *testing.T) {
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{NodeID: "n1", Processes: []batch.ProcView{{ProcessID: "p1"}}}}},
		Specs:   memSpecs{"n1/p1": {ProcessID: "p1", Name: "x", NodeID: "n1", LatestRevision: 3, SpecJSON: `{}`}},
		Auth:    allowAuth{},
		ConfigOverlay: func(s batch.OwnerSpec) (string, int64, error) {
			if s.ProcessID != "p1" {
				t.Fatalf("spec %+v", s)
			}
			return `{"expected_revision":3}`, 3, nil
		},
	}
	ts, err := x.Expand(context.Background(), batch.Selector{ProcessIDs: []string{"p1"}}, batch.TypeConfigUpdate)
	if err != nil || len(ts) != 1 || ts[0].PayloadJSON == "" || ts[0].ExpectedRevision != 3 {
		t.Fatalf("%+v %v", ts, err)
	}
	if ts[0].Status != "" && ts[0].Status != batch.TargetPending {
		t.Fatalf("status %s", ts[0].Status)
	}

	x.ConfigOverlay = func(batch.OwnerSpec) (string, int64, error) {
		return "", 0, errcode.E(errcode.INVALID, "overlay")
	}
	ts, err = x.Expand(context.Background(), batch.Selector{ProcessIDs: []string{"p1"}}, batch.TypeConfigUpdate)
	if err != nil || len(ts) != 1 || ts[0].Status != batch.TargetInvalid {
		t.Fatalf("overlay fail: %+v %v", ts, err)
	}
}

func TestExpand_WorkerSkipsDeniedAndInvalid(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	x := &batch.RealExpander{
		Cluster: memCluster{nodes: []batch.NodeView{{
			NodeID: "n1",
			Processes: []batch.ProcView{
				{ProcessID: "p-ok", Name: "ok", Group: "ads"},
				{ProcessID: "p-deny", Name: "no", Group: "finance"},
			},
		}}},
		Specs: memSpecs{
			"n1/p-ok":   {ProcessID: "p-ok", Name: "ok", NodeID: "n1", Group: "ads"},
			"n1/p-deny": {ProcessID: "p-deny", Name: "no", NodeID: "n1", Group: "finance"},
		},
		Auth: denyGroup("finance"),
	}
	var mu sync.Mutex
	var ran []string
	seq := 0
	e := &batch.Engine{
		DB: st, SourceAgent: "n1", Expand: x,
		Exec: stubExec{fn: func(_ context.Context, tg batch.Target) error {
			mu.Lock()
			ran = append(ran, tg.ProcessID)
			mu.Unlock()
			return nil
		}},
		NewID: func() string {
			seq++
			return "id-" + strconv.Itoa(seq)
		},
	}
	e.Start(ctx)
	b, err := e.Create(ctx, "admin", batch.TypeRestart, batch.Selector{ProcessIDs: []string{"p-ok", "p-deny", "p-miss"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitBatch(t, e, b.BatchID, batch.StatusPartial)
	if len(got.Targets) != 3 {
		t.Fatalf("targets %+v", got.Targets)
	}
	byID := map[string]batch.Target{}
	for _, tg := range got.Targets {
		byID[tg.ProcessID] = tg
	}
	if byID["p-ok"].Status != batch.TargetSuccess {
		t.Fatalf("ok %+v", byID["p-ok"])
	}
	if byID["p-deny"].Status != batch.TargetDenied {
		t.Fatalf("deny %+v", byID["p-deny"])
	}
	if byID["p-miss"].Status != batch.TargetInvalid {
		t.Fatalf("miss %+v", byID["p-miss"])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 1 || ran[0] != "p-ok" {
		t.Fatalf("exec ran %v", ran)
	}
}
