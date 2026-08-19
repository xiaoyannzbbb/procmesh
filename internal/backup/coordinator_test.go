package backup_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
)

type fakeControl struct {
	mu      sync.Mutex
	pol     []backup.PolicyView
	claims  map[string]string
	claimN  int
	updates []backup.TaskUpdate
}

func (f *fakeControl) ListEnabledBackupPolicies(context.Context) ([]backup.PolicyView, error) {
	return f.pol, nil
}
func (f *fakeControl) ClaimFire(_ context.Context, key, _ string, _ uint64, _ time.Time) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimN++
	if id := f.claims[key]; id != "" {
		return id, false, nil
	}
	id := "run-" + key
	f.claims[key] = id
	return id, true, nil
}
func (f *fakeControl) UpdateTask(_ context.Context, u backup.TaskUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, u)
	return nil
}

type fakeDispatcher struct {
	mu       sync.Mutex
	tasks    []backup.BackupTaskRequest
	errNodes map[string]bool
}

func (f *fakeDispatcher) DispatchBackupTask(_ context.Context, task backup.BackupTaskRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, task)
	if f.errNodes[task.NodeID] {
		return errors.New("dispatch failed")
	}
	return nil
}

func TestCoordinator_LeaderOnlyAndIdempotentTick(t *testing.T) {
	now := time.Date(2026, 8, 19, 2, 0, 20, 0, time.UTC)
	ctrl := &fakeControl{claims: map[string]string{}, pol: []backup.PolicyView{{Policy: backup.Policy{PolicyID: "bp", Enabled: true, ScheduleCron: "0 * * * *", Timezone: "UTC", Sink: "fs", MaxConcurrency: 2}, Revision: 3, TargetNodeIDs: []string{"a", "b"}}}}
	disp := &fakeDispatcher{}
	leader := true
	c := backup.NewCoordinator(backup.CoordinatorConfig{Control: ctrl, Dispatcher: disp, IsLeader: func() bool { return leader }, CurrentTerm: func() uint64 { return 7 }, Now: func() time.Time { return now }})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(disp.tasks) != 2 || ctrl.claimN != 1 {
		t.Fatalf("tasks=%d claims=%d", len(disp.tasks), ctrl.claimN)
	}
	leader = false
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ctrl.claimN != 1 {
		t.Fatalf("follower claimed: %d", ctrl.claimN)
	}
}

func TestCoordinator_LeaderTermReusesRunAndTaskIDs(t *testing.T) {
	now := time.Date(2026, 8, 19, 2, 0, 20, 0, time.UTC)
	ctrl := &fakeControl{claims: map[string]string{}, pol: []backup.PolicyView{{Policy: backup.Policy{PolicyID: "bp", Enabled: true, ScheduleCron: "0 * * * *", Timezone: "UTC", Sink: "fs"}, Revision: 1, TargetNodeIDs: []string{"a"}}}}
	disp := &fakeDispatcher{}
	term := uint64(1)
	c := backup.NewCoordinator(backup.CoordinatorConfig{Control: ctrl, Dispatcher: disp, IsLeader: func() bool { return true }, CurrentTerm: func() uint64 { return term }, Now: func() time.Time { return now }})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	term = 2
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(disp.tasks) != 2 || disp.tasks[0].RunID != disp.tasks[1].RunID || disp.tasks[0].TaskID != disp.tasks[1].TaskID {
		t.Fatalf("tasks=%+v", disp.tasks)
	}
}

func TestCoordinator_DelayedTickCatchesCurrentDueFire(t *testing.T) {
	now := time.Date(2026, 8, 19, 2, 2, 20, 0, time.UTC)
	ctrl := &fakeControl{claims: map[string]string{}, pol: []backup.PolicyView{{Policy: backup.Policy{PolicyID: "bp", Enabled: true, ScheduleCron: "0 * * * *", Timezone: "UTC", Sink: "fs"}, Revision: 1, TargetNodeIDs: []string{"a"}}}}
	disp := &fakeDispatcher{}
	c := backup.NewCoordinator(backup.CoordinatorConfig{Control: ctrl, Dispatcher: disp, IsLeader: func() bool { return true }, CurrentTerm: func() uint64 { return 1 }, Now: func() time.Time { return now }})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(disp.tasks) != 1 || ctrl.claimN != 1 {
		t.Fatalf("delayed tick missed fire: tasks=%d claims=%d", len(disp.tasks), ctrl.claimN)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(disp.tasks) != 1 || ctrl.claimN != 1 {
		t.Fatalf("same minute not idempotent: tasks=%d claims=%d", len(disp.tasks), ctrl.claimN)
	}
}

func TestCoordinator_ReusedFireSkipsRunCreateAndStillDispatches(t *testing.T) {
	now := time.Date(2026, 8, 19, 2, 0, 20, 0, time.UTC)
	ctrl := &fakeControl{claims: map[string]string{}, pol: []backup.PolicyView{{Policy: backup.Policy{PolicyID: "bp", Enabled: true, ScheduleCron: "0 * * * *", Timezone: "UTC", Sink: "fs"}, Revision: 1, TargetNodeIDs: []string{"a"}}}}
	disp := &fakeDispatcher{}
	created := 0
	c := backup.NewCoordinator(backup.CoordinatorConfig{
		Control: ctrl, Dispatcher: disp, IsLeader: func() bool { return true }, CurrentTerm: func() uint64 { return 1 }, Now: func() time.Time { return now },
		RunCreator: runCreatorFunc(func(context.Context, backup.BackupRunRequest) error { created++; return nil }),
	})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if created != 1 || len(disp.tasks) != 1 {
		t.Fatalf("created=%d tasks=%d", created, len(disp.tasks))
	}
	// A new term reclaims the same durable fire and must dispatch its stable
	// task identity without attempting to recreate the immutable run record.
	c.CurrentTerm = func() uint64 { return 2 }
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if created != 1 || len(disp.tasks) != 2 || disp.tasks[0].RunID != disp.tasks[1].RunID || disp.tasks[0].TaskID != disp.tasks[1].TaskID {
		t.Fatalf("created=%d tasks=%+v", created, disp.tasks)
	}
}

type runCreatorFunc func(context.Context, backup.BackupRunRequest) error

func (f runCreatorFunc) CreateBackupRun(ctx context.Context, req backup.BackupRunRequest) error {
	return f(ctx, req)
}

func TestCoordinator_DispatchErrorUpdatesTask(t *testing.T) {
	now := time.Date(2026, 8, 19, 2, 0, 20, 0, time.UTC)
	ctrl := &fakeControl{claims: map[string]string{}, pol: []backup.PolicyView{{Policy: backup.Policy{PolicyID: "bp", Enabled: true, ScheduleCron: "0 * * * *", Timezone: "UTC", Sink: "fs"}, Revision: 1, TargetNodeIDs: []string{"a", "b"}}}}
	disp := &fakeDispatcher{errNodes: map[string]bool{"a": true}}
	c := backup.NewCoordinator(backup.CoordinatorConfig{Control: ctrl, Dispatcher: disp, IsLeader: func() bool { return true }, CurrentTerm: func() uint64 { return 1 }, Now: func() time.Time { return now }})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(disp.tasks) != 2 || len(ctrl.updates) != 1 || ctrl.updates[0].Status != "UNAVAILABLE" {
		t.Fatalf("tasks=%d updates=%+v", len(disp.tasks), ctrl.updates)
	}
}

func TestClusterSchedule_NextTimezone(t *testing.T) {
	from := time.Date(2026, 8, 19, 9, 59, 0, 0, time.UTC)
	got, err := backup.NextInTimezone("0 18 * * *", "Asia/Shanghai", from)
	if err != nil || !got.Equal(time.Date(2026, 8, 19, 18, 0, 0, 0, time.FixedZone("CST", 8*60*60))) {
		t.Fatalf("got=%v err=%v", got, err)
	}
	strict, err := backup.Next("0 * * * *", time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC))
	if err != nil || !strict.After(time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("strict=%v err=%v", strict, err)
	}
	previous, err := backup.PreviousOrEqualInTimezone("0 * * * *", "UTC", time.Date(2026, 8, 19, 2, 2, 0, 0, time.UTC))
	if err != nil || !previous.Equal(time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("previous=%v err=%v", previous, err)
	}
}
