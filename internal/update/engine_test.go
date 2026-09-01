package update_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/cluster"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
	"github.com/qleelulu/procmesh/internal/update"
)

func testPin() update.Pin {
	return update.Pin{
		Repository: "owner/procmesh",
		Tag:        "v0.2.0",
		Checksums: map[string]string{
			"linux/amd64": "a", "linux/arm64": "b", "linux/armv7": "c",
		},
	}
}

func openUpdateStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func waitJob(t *testing.T, e *update.Engine, id string, want update.JobStatus) update.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last update.Job
	for time.Now().Before(deadline) {
		got, err := e.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		last = got
		if got.Status == want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wait status %s: last=%+v targets=%+v", want, last, last.Targets)
	return update.Job{}
}

func waitTarget(t *testing.T, e *update.Engine, id, nodeID string, want update.TargetStatus) update.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last update.Job
	for time.Now().Before(deadline) {
		got, err := e.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		last = got
		for _, target := range got.Targets {
			if target.NodeID == nodeID && target.Status == want {
				return got
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wait target %s status %s: job=%+v targets=%+v", nodeID, want, last, last.Targets)
	return update.Job{}
}

type memClock struct {
	mu      sync.Mutex
	now     time.Time
	members []cluster.NodeSummary
}

func (c *memClock) Members() []cluster.NodeSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]cluster.NodeSummary, len(c.members))
	copy(out, c.members)
	return out
}

func (c *memClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *memClock) setVersion(nodeID, ver string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.members {
		if c.members[i].NodeID == nodeID {
			c.members[i].AgentVersion = ver
			c.members[i].State = cluster.StateAlive
			c.members[i].LastUpdatedUnixMs = c.now.UnixMilli()
		}
	}
}

func liveNode(id, host, ver string, now time.Time) cluster.NodeSummary {
	return cluster.NodeSummary{
		NodeID:            id,
		Hostname:          host,
		State:             cluster.StateAlive,
		OS:                "linux",
		Arch:              "amd64",
		AgentVersion:      ver,
		LastUpdatedUnixMs: now.UnixMilli(),
	}
}

type fakeApplier struct {
	mu      sync.Mutex
	order   []string
	ops     []string
	err     map[string]error
	block   map[string]chan struct{}
	clock   *memClock
	noBump  map[string]bool
	started chan string
}

type confirmationLostApplier struct {
	clock *memClock
	err   error
}

func (a confirmationLostApplier) Apply(_ context.Context, nodeID string, pin update.Pin, _ string) error {
	a.clock.setVersion(nodeID, pin.Tag)
	return a.err
}

func (f *fakeApplier) Apply(_ context.Context, nodeID string, pin update.Pin, operationID string) error {
	f.mu.Lock()
	f.order = append(f.order, nodeID)
	f.ops = append(f.ops, operationID)
	if f.started != nil {
		select {
		case f.started <- nodeID:
		default:
		}
	}
	block := f.block[nodeID]
	applyErr := f.err[nodeID]
	noBump := f.noBump[nodeID]
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	if applyErr != nil {
		return applyErr
	}
	if !noBump && f.clock != nil {
		f.clock.setVersion(nodeID, pin.Tag)
	}
	return nil
}

func (f *fakeApplier) applied() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.order))
	copy(out, f.order)
	return out
}

func newEngine(t *testing.T, clock *memClock, apply update.NodeApplier, ids ...string) *update.Engine {
	t.Helper()
	st := openUpdateStore(t)
	seq := ids
	e := &update.Engine{
		DB:           st,
		Apply:        apply,
		Members:      clock,
		SourceAgent:  "entry",
		WaitTimeout:  400 * time.Millisecond,
		PollInterval: 10 * time.Millisecond,
		NewID: func() string {
			if len(seq) == 0 {
				t.Fatal("out of test ids")
			}
			id := seq[0]
			seq = seq[1:]
			return id
		},
	}
	e.Start(context.Background())
	return e
}

func TestEngine_ApplyConfirmationLossWaitsForObservedVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "timeout", err: errcode.E(errcode.TIMEOUT, "rpc timed out")},
		{name: "unavailable", err: errcode.E(errcode.UNAVAILABLE, "owner unreachable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(1_700_000_000, 0)
			clock := &memClock{now: now, members: []cluster.NodeSummary{
				liveNode("n1", "h1", "0.1.0", now),
			}}
			e := newEngine(t, clock, confirmationLostApplier{clock: clock, err: tc.err}, "j1", "op1")
			job, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{{NodeID: "n1", Hostname: "h1"}}, "", "")
			if err != nil {
				t.Fatal(err)
			}
			got := waitJob(t, e, job.JobID, update.JobCompleted)
			if got.Targets[0].Status != update.TargetSuccess || got.Targets[0].Error != "" {
				t.Fatalf("target=%+v", got.Targets[0])
			}
		})
	}
}

func TestEngine_ResumeReconcilesObservedRunningTargetWithoutReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	clock := &memClock{now: now, members: []cluster.NodeSummary{
		liveNode("entry", "host-entry", testPin().Tag, now),
	}}
	st := openUpdateStore(t)
	pinJSON := `{"repository":"owner/procmesh","tag":"v0.2.0","checksums":{"linux/amd64":"a","linux/arm64":"b","linux/armv7":"c"}}`
	if err := st.InsertUpdateJob(ctx, store.UpdateJobRecord{
		JobID: "job-resume", Operator: "admin", SourceAgent: "entry", PinJSON: pinJSON,
		CreatedAt: now.Add(-time.Minute), StartedAt: now.Add(-time.Minute), FinishedAt: now,
		Status: string(update.JobCompleted), SummaryJSON: `{}`,
	}, []store.UpdateJobTargetRecord{{
		JobID: "job-resume", OperationID: "op-entry", NodeID: "entry", Hostname: "host-entry",
		Status: string(update.TargetRunning), StartedAt: now.Add(-30 * time.Second),
	}}); err != nil {
		t.Fatal(err)
	}

	apply := &fakeApplier{clock: clock}
	e := &update.Engine{
		DB: st, Apply: apply, Members: clock, SourceAgent: "entry",
		WaitTimeout: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond,
	}
	e.Start(ctx)
	if err := e.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	waitTarget(t, e, "job-resume", "entry", update.TargetSuccess)
	got := waitJob(t, e, "job-resume", update.JobCompleted)
	if got.Status != update.JobCompleted {
		t.Fatalf("resumed job=%s, want COMPLETED", got.Status)
	}
	if got.Targets[0].Status != update.TargetSuccess {
		t.Fatalf("resumed target=%s, want SUCCESS", got.Targets[0].Status)
	}
	if len(apply.applied()) != 0 {
		t.Fatalf("recovery must observe, not replay apply: %v", apply.applied())
	}
}

func TestEngine_ResumeReconcilesRunningTargetAfterEarlierFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	clock := &memClock{now: now, members: []cluster.NodeSummary{
		liveNode("entry", "host-entry", testPin().Tag, now),
	}}
	st := openUpdateStore(t)
	pinJSON := `{"repository":"owner/procmesh","tag":"v0.2.0","checksums":{"linux/amd64":"a","linux/arm64":"b","linux/armv7":"c"}}`
	if err := st.InsertUpdateJob(ctx, store.UpdateJobRecord{
		JobID: "job-partial-resume", Operator: "admin", SourceAgent: "entry", PinJSON: pinJSON,
		CreatedAt: now.Add(-time.Minute), StartedAt: now.Add(-time.Minute), FinishedAt: now,
		Status: string(update.JobPartial), SummaryJSON: `{"failed":1}`,
	}, []store.UpdateJobTargetRecord{
		{JobID: "job-partial-resume", OperationID: "op-failed", NodeID: "failed", Status: string(update.TargetFailed)},
		{JobID: "job-partial-resume", OperationID: "op-entry", NodeID: "entry", Hostname: "host-entry", Status: string(update.TargetRunning), OrderIndex: 1, StartedAt: now.Add(-30 * time.Second)},
	}); err != nil {
		t.Fatal(err)
	}

	e := &update.Engine{
		DB: st, Apply: &fakeApplier{clock: clock}, Members: clock, SourceAgent: "entry",
		WaitTimeout: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond,
	}
	e.Start(ctx)
	if err := e.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	got := waitTarget(t, e, "job-partial-resume", "entry", update.TargetSuccess)
	if got.Status != update.JobPartial {
		t.Fatalf("resumed job=%s, want PARTIAL", got.Status)
	}
}

func TestEngine_CreateRejectsEmptyOperatorAndPin(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{liveNode("n1", "h1", "0.1.0", now)}}
	e := newEngine(t, clock, &fakeApplier{clock: clock}, "j1", "op1")
	_, err := e.Create(ctx, "", testPin(), []update.TargetSpec{{NodeID: "n1", Hostname: "h1"}}, "", "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("empty operator: %v", err)
	}
	_, err = e.Create(ctx, "admin", update.Pin{Tag: "v0.2.0"}, []update.TargetSpec{{NodeID: "n1"}}, "", "")
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("bad pin: %v", err)
	}
}

func TestEngine_OneRunningJob(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{
		liveNode("n1", "h1", "0.1.0", now),
	}}
	hold := make(chan struct{})
	apply := &fakeApplier{clock: clock, block: map[string]chan struct{}{"n1": hold}, started: make(chan string, 1)}
	e := newEngine(t, clock, apply, "j1", "op1", "j2", "op2")
	first, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{{NodeID: "n1", Hostname: "h1"}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-apply.started:
	case <-time.After(time.Second):
		t.Fatal("first apply not started")
	}
	_, err = e.Create(ctx, "admin", testPin(), []update.TargetSpec{{NodeID: "n1", Hostname: "h1"}}, "", "")
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT, got %v", err)
	}
	close(hold)
	waitJob(t, e, first.JobID, update.JobCompleted)
}

func TestEngine_OrderAndSerialWait(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{
		liveNode("leader", "aaa", "0.1.0", now),
		liveNode("entry", "zzz", "0.1.0", now),
		liveNode("a", "host-a", "0.1.0", now),
	}}
	apply := &fakeApplier{clock: clock}
	e := newEngine(t, clock, apply, "j1", "op-a", "op-entry", "op-leader")
	job, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{
		{NodeID: "leader", Hostname: "aaa"},
		{NodeID: "entry", Hostname: "zzz"},
		{NodeID: "a", Hostname: "host-a"},
	}, "leader", "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitJob(t, e, job.JobID, update.JobCompleted)
	if applyOrder := apply.applied(); len(applyOrder) != 3 || applyOrder[0] != "a" || applyOrder[1] != "leader" || applyOrder[2] != "entry" {
		t.Fatalf("apply order %v", applyOrder)
	}
	if got.Summary.Success != 3 {
		t.Fatalf("summary %+v", got.Summary)
	}
}

func TestEngine_SkipsAreNotFailures(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now}
	e := newEngine(t, clock, &fakeApplier{clock: clock}, "j1", "op-skip")
	job, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{
		{NodeID: "mac", Hostname: "m", SkipReason: update.SkipMACOS},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != update.JobCompleted {
		got := waitJob(t, e, job.JobID, update.JobCompleted)
		job = got
	}
	if job.Targets[0].Status != update.TargetSkipped || job.Summary.Skipped != 1 || job.Summary.Failed != 0 {
		t.Fatalf("%+v", job)
	}
}

func TestEngine_TimeoutStopsRemaining(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{
		liveNode("n1", "h1", "0.1.0", now),
		liveNode("n2", "h2", "0.1.0", now),
	}}
	apply := &fakeApplier{clock: clock, noBump: map[string]bool{"n1": true}}
	e := newEngine(t, clock, apply, "j1", "op1", "op2")
	job, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{
		{NodeID: "n1", Hostname: "h1"},
		{NodeID: "n2", Hostname: "h2"},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitJob(t, e, job.JobID, update.JobFailed)
	byID := map[string]update.Target{}
	for _, tg := range got.Targets {
		byID[tg.NodeID] = tg
	}
	if byID["n1"].Status != update.TargetTimeout {
		t.Fatalf("n1=%s", byID["n1"].Status)
	}
	if byID["n2"].Status != update.TargetPending {
		t.Fatalf("n2 remaining want PENDING got %s", byID["n2"].Status)
	}
	if len(apply.applied()) != 1 {
		t.Fatalf("should stop remaining applies %v", apply.applied())
	}
}

func TestEngine_ConflictStopsRemainingPartial(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{
		liveNode("n1", "h1", "0.1.0", now),
		liveNode("n2", "h2", "0.1.0", now),
		liveNode("n3", "h3", "0.1.0", now),
	}}
	apply := &fakeApplier{
		clock: clock,
		err:   map[string]error{"n2": errcode.E(errcode.CONFLICT, "update already in progress")},
	}
	e := newEngine(t, clock, apply, "j1", "op1", "op2", "op3")
	job, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{
		{NodeID: "n1", Hostname: "h1"},
		{NodeID: "n2", Hostname: "h2"},
		{NodeID: "n3", Hostname: "h3"},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitJob(t, e, job.JobID, update.JobPartial)
	byID := map[string]update.Target{}
	for _, tg := range got.Targets {
		byID[tg.NodeID] = tg
	}
	if byID["n1"].Status != update.TargetSuccess || byID["n2"].Status != update.TargetConflict || byID["n3"].Status != update.TargetPending {
		t.Fatalf("%+v", byID)
	}
}

func TestEngine_NoneSucceedFailed(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{liveNode("n1", "h1", "0.1.0", now)}}
	apply := &fakeApplier{clock: clock, err: map[string]error{"n1": errcode.E(errcode.UNAVAILABLE, "update failed")}}
	e := newEngine(t, clock, apply, "j1", "op1")
	job, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{{NodeID: "n1", Hostname: "h1"}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitJob(t, e, job.JobID, update.JobFailed)
	if got.Targets[0].Status != update.TargetFailed {
		t.Fatalf("%+v", got.Targets[0])
	}
}

func TestEngine_CancelRemainingKeepsInFlight(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{
		liveNode("n1", "h1", "0.1.0", now),
		liveNode("n2", "h2", "0.1.0", now),
	}}
	hold := make(chan struct{})
	apply := &fakeApplier{clock: clock, block: map[string]chan struct{}{"n1": hold}, started: make(chan string, 1)}
	e := newEngine(t, clock, apply, "j1", "op1", "op2")
	job, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{
		{NodeID: "n1", Hostname: "h1"},
		{NodeID: "n2", Hostname: "h2"},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-apply.started:
	case <-time.After(time.Second):
		t.Fatal("in-flight not started")
	}
	if _, err := e.CancelRemaining(ctx, job.JobID, "admin"); err != nil {
		t.Fatal(err)
	}
	close(hold)
	got := waitJob(t, e, job.JobID, update.JobCompleted)
	byID := map[string]update.Target{}
	for _, tg := range got.Targets {
		byID[tg.NodeID] = tg
	}
	if byID["n1"].Status != update.TargetSuccess {
		t.Fatalf("in-flight %s", byID["n1"].Status)
	}
	if byID["n2"].Status != update.TargetCancelled {
		t.Fatalf("remaining %s", byID["n2"].Status)
	}
}

func TestEngine_RetrySamePinAndKeepSkips(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{
		liveNode("n1", "h1", "0.1.0", now),
		liveNode("n2", "h2", "0.1.0", now),
	}}
	apply := &fakeApplier{
		clock: clock,
		err:   map[string]error{"n1": errcode.E(errcode.UNAVAILABLE, "update failed")},
	}
	e := newEngine(t, clock, apply, "j1", "op-skip", "op1", "op2", "op1b", "op2b")
	job, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{
		{NodeID: "mac", Hostname: "m", SkipReason: update.SkipMACOS},
		{NodeID: "n1", Hostname: "h1"},
		{NodeID: "n2", Hostname: "h2"},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitJob(t, e, job.JobID, update.JobFailed)
	origSkipOp := ""
	for _, tg := range failed.Targets {
		if tg.NodeID == "mac" {
			origSkipOp = tg.OperationID
		}
	}
	apply.mu.Lock()
	delete(apply.err, "n1")
	apply.mu.Unlock()
	retried, err := e.Retry(ctx, job.JobID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if retried.Pin.Tag != testPin().Tag {
		t.Fatalf("pin changed %+v", retried.Pin)
	}
	got := waitJob(t, e, job.JobID, update.JobCompleted)
	byID := map[string]update.Target{}
	for _, tg := range got.Targets {
		byID[tg.NodeID] = tg
	}
	if byID["mac"].Status != update.TargetSkipped || byID["mac"].SkipReason != update.SkipMACOS || byID["mac"].OperationID != origSkipOp {
		t.Fatalf("skip set changed %+v", byID["mac"])
	}
	if byID["n1"].Status != update.TargetSuccess || byID["n2"].Status != update.TargetSuccess {
		t.Fatalf("%+v", byID)
	}
}

func TestEngine_RetryAlreadyAtPinIsSuccessNoOp(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{liveNode("n1", "h1", "0.1.0", now)}}
	apply := &fakeApplier{clock: clock, err: map[string]error{"n1": errcode.E(errcode.UNAVAILABLE, "update failed")}}
	e := newEngine(t, clock, apply, "j1", "op1", "op1b")
	job, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{{NodeID: "n1", Hostname: "h1"}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, e, job.JobID, update.JobFailed)
	clock.setVersion("n1", "v0.2.0")
	apply.mu.Lock()
	delete(apply.err, "n1")
	apply.mu.Unlock()
	if _, err := e.Retry(ctx, job.JobID, "admin"); err != nil {
		t.Fatal(err)
	}
	got := waitJob(t, e, job.JobID, update.JobCompleted)
	if got.Targets[0].Status != update.TargetSuccess {
		t.Fatalf("%+v", got.Targets[0])
	}
}

func TestEngine_ListOmitsTargetsAndClamps(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now}
	e := newEngine(t, clock, &fakeApplier{clock: clock}, "j1", "op1", "j2", "op2")
	if _, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{
		{NodeID: "mac", Hostname: "m", SkipReason: update.SkipCURRENT},
	}, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{
		{NodeID: "mac2", Hostname: "m2", SkipReason: update.SkipMACOS},
	}, "", ""); err != nil {
		t.Fatal(err)
	}
	list, err := e.List(ctx, 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("%+v %v", list, err)
	}
	for _, j := range list {
		if len(j.Targets) != 0 {
			t.Fatalf("list must omit targets %+v", j)
		}
	}
	list, err = e.List(ctx, 1)
	if err != nil || len(list) != 1 {
		t.Fatalf("limit 1 %+v %v", list, err)
	}
}

func TestJobRollup(t *testing.T) {
	t.Parallel()
	if update.RollupJob([]update.Target{
		{Status: update.TargetSkipped}, {Status: update.TargetSuccess},
	}, true) != update.JobCompleted {
		t.Fatal("all attempted success")
	}
	if update.RollupJob([]update.Target{
		{Status: update.TargetSuccess}, {Status: update.TargetFailed}, {Status: update.TargetPending},
	}, true) != update.JobPartial {
		t.Fatal("some attempted fail")
	}
	if update.RollupJob([]update.Target{
		{Status: update.TargetTimeout}, {Status: update.TargetPending},
	}, true) != update.JobFailed {
		t.Fatal("none succeed")
	}
	if update.RollupJob([]update.Target{
		{Status: update.TargetSkipped}, {Status: update.TargetCancelled},
	}, true) != update.JobCompleted {
		t.Fatal("skips and cancel without failed attempts")
	}
	if update.RollupJob([]update.Target{
		{Status: update.TargetSkipped},
	}, true) != update.JobCompleted {
		t.Fatal("all skip")
	}
	if update.RollupJob([]update.Target{
		{Status: update.TargetSuccess}, {Status: update.TargetPending},
	}, false) != update.JobRunning {
		t.Fatal("pending without stop stays running")
	}
	if update.RollupJob([]update.Target{
		{Status: update.TargetSuccess}, {Status: update.TargetRunning},
	}, true) != update.JobRunning {
		t.Fatal("running target must keep a stopped job running until recovery")
	}
}

func TestEngine_CreateSameOperationIDReturnsExisting(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now}
	e := newEngine(t, clock, &fakeApplier{clock: clock}, "j1", "op-skip", "j2", "op-skip2")
	first, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{
		{NodeID: "mac", Hostname: "m", SkipReason: update.SkipMACOS},
	}, "", "op-create")
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID != "op-create" {
		t.Fatalf("operation_id=%q", first.OperationID)
	}
	second, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{
		{NodeID: "mac", Hostname: "m", SkipReason: update.SkipMACOS},
	}, "", "op-create")
	if err != nil {
		t.Fatal(err)
	}
	if second.JobID != first.JobID {
		t.Fatalf("idempotent job %s vs %s", second.JobID, first.JobID)
	}
	list, err := e.List(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("jobs=%d err=%v", len(list), err)
	}
}

func TestEngine_CreateSameOperationIDDoesNotInsertSecondRunning(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{liveNode("n1", "h1", "0.1.0", now)}}
	hold := make(chan struct{})
	apply := &fakeApplier{clock: clock, block: map[string]chan struct{}{"n1": hold}, started: make(chan string, 1)}
	e := newEngine(t, clock, apply, "j1", "op1", "j2", "op2")
	first, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{{NodeID: "n1", Hostname: "h1"}}, "", "op-run")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-apply.started:
	case <-time.After(time.Second):
		t.Fatal("first apply not started")
	}
	second, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{{NodeID: "n1", Hostname: "h1"}}, "", "op-run")
	if err != nil {
		t.Fatal(err)
	}
	if second.JobID != first.JobID || second.Status != update.JobRunning {
		t.Fatalf("second %+v first=%s", second, first.JobID)
	}
	list, err := e.List(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("jobs=%d err=%v", len(list), err)
	}
	close(hold)
	waitJob(t, e, first.JobID, update.JobCompleted)
}

func TestEngine_BindTargetsBeforeEnqueue(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{liveNode("n1", "h1", "0.1.0", now)}}
	hold := make(chan struct{})
	apply := &fakeApplier{clock: clock, block: map[string]chan struct{}{"n1": hold}, started: make(chan string, 1)}
	e := newEngine(t, clock, apply, "j1", "op1")
	var bound []string
	applyBeforeBind := false
	e.BindTargets = func(_ context.Context, targets []update.Target) {
		for _, tg := range targets {
			bound = append(bound, tg.OperationID)
		}
	}
	e.Apply = &bindCheckApplier{inner: apply, bound: &bound, before: &applyBeforeBind}
	if _, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{{NodeID: "n1", Hostname: "h1"}}, "", "op-bind"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-apply.started:
	case <-time.After(time.Second):
		t.Fatal("apply not started")
	}
	if applyBeforeBind {
		t.Fatal("Apply ran before BindTargets")
	}
	if len(bound) != 1 || bound[0] != "op1" {
		t.Fatalf("BindTargets=%v", bound)
	}
	close(hold)
	waitJob(t, e, "j1", update.JobCompleted)
}

func TestEngine_RetryBindsRemintsBeforeEnqueue(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := &memClock{now: now, members: []cluster.NodeSummary{liveNode("n1", "h1", "0.1.0", now)}}
	apply := &fakeApplier{clock: clock, err: map[string]error{"n1": errcode.E(errcode.UNAVAILABLE, "update failed")}}
	e := newEngine(t, clock, apply, "j1", "op-old", "op-new")
	job, err := e.Create(ctx, "admin", testPin(), []update.TargetSpec{{NodeID: "n1", Hostname: "h1"}}, "", "op-retry")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, e, job.JobID, update.JobFailed)

	var bound []string
	e.BindTargets = func(_ context.Context, targets []update.Target) {
		for _, tg := range targets {
			bound = append(bound, tg.OperationID)
		}
	}
	apply.mu.Lock()
	delete(apply.err, "n1")
	apply.mu.Unlock()
	if _, err := e.Retry(ctx, job.JobID, "admin"); err != nil {
		t.Fatal(err)
	}
	waitJob(t, e, job.JobID, update.JobCompleted)
	if len(bound) != 1 || bound[0] != "op-new" {
		t.Fatalf("remint BindTargets=%v", bound)
	}
}

type bindCheckApplier struct {
	inner  *fakeApplier
	bound  *[]string
	before *bool
}

func (b *bindCheckApplier) Apply(ctx context.Context, nodeID string, pin update.Pin, operationID string) error {
	if len(*b.bound) == 0 || (*b.bound)[0] != operationID {
		*b.before = true
	}
	return b.inner.Apply(ctx, nodeID, pin, operationID)
}
