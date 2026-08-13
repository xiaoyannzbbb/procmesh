# P0 Local Process Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a Linux-first `procmesh-agent` + `procmesh-shim` that locally creates, starts, stops, restarts, and recovers processes with revision CAS, restart policy, health checks, file logs, and local audit — without any cluster, Raft, or Web UI.

**Architecture:** Agent owns desired state in SQLite and reconciles instances through a per-instance `procmesh-shim` over a length-prefixed protobuf Unix socket. Shim is the only process that fork/execs the business command and must `setsid()` so Agent death does not kill children. Reconcile never double-starts a live process; a dead shim with a live PID becomes `UNKNOWN` until explicit `Adopt`.

**Tech Stack:** Go 1.23, `modernc.org/sqlite`, `google.golang.org/protobuf`, `gopkg.in/yaml.v3` (spec fixtures only). No ConnectRPC, memberlist, raft, or Vue in this phase.

## Global Constraints

- Module path: `github.com/qleelulu/procmesh`
- Go version floor: `1.23`
- CGO-free SQLite only: `modernc.org/sqlite` (never `mattn/go-sqlite3`)
- Linux is the production guarantee; macOS must compile and run unit tests plus non-cgroup integration
- `run_as_user` on macOS or without permission: return `errcode.Error{Code: errcode.INVALID}` — do not skip silently
- `resource_limit` on macOS: ignore and record an audit warning `RESOURCE_LIMIT_UNSUPPORTED`
- Agent stop must not kill shim or business processes (`KillMode=process` later; shim must `setsid`)
- `process` must not import `cluster` or `control` (those packages do not exist yet; do not create them)
- Logs are files, never SQLite blobs
- Every mutation that goes through `Manager` requires a non-empty `operation_id`
- Config writes require `expected_revision` (`0` only for create)
- Default data dir Linux: `/var/lib/procmesh` (tests use `t.TempDir()`; agent `--data-dir`)
- Shim binary discovered as `$PATH/procmesh-shim` or same directory as the agent executable
- Protocol version constant: `internal/version.Protocol = 1`
- Language of user-facing errors: English codes, English messages (CLI i18n is not P0)
- No Windows code paths
- Tests live next to code: `internal/foo/foo_test.go`
- Coverage floors this phase: `internal/process`, `internal/shim`, `internal/store` ≥ 80%
- Do not add Agent Groups, alerts, batch ops, or HTTP auth

## File map (create in the tasks below)

```text
go.mod
go.sum
Makefile
cmd/procmesh-agent/main.go
cmd/procmesh-shim/main.go
proto/shim/v1/shim.proto
internal/version/version.go
internal/errcode/code.go
internal/paths/paths.go
internal/process/types.go
internal/process/validate.go
internal/process/machine.go
internal/process/restart.go
internal/process/deps.go
internal/process/manager.go
internal/store/store.go
internal/store/schema.sql
internal/store/spec.go
internal/store/instance.go
internal/store/journal.go
internal/store/audit.go
internal/store/meta.go
internal/shim/frame.go
internal/shim/client.go
internal/shim/server.go
internal/shim/launch.go
internal/logmgr/logmgr.go
internal/health/health.go
internal/localhttp/server.go
deployments/systemd/procmesh-agent.service
```

Generated (do not hand-edit): `proto/shim/v1/shim.pb.go` via `protoc` or `buf`.

---

### Task 1: Module, error codes, domain types

**Files:**
- Create: `go.mod`
- Create: `internal/version/version.go`
- Create: `internal/errcode/code.go`
- Create: `internal/process/types.go`
- Create: `internal/process/validate.go`
- Create: `internal/process/validate_test.go`
- Create: `Makefile`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `version.Protocol int = 1`
  - `errcode.Code` string constants: `OK`, `CONFLICT`, `UNAVAILABLE`, `TIMEOUT`, `DENIED`, `DEGRADED`, `DUPLICATE_NODE_ID`, `INCOMPATIBLE_VERSION`, `NOT_FOUND`, `INVALID`
  - `func errcode.E(code Code, msg string) error` returning `*errcode.Error`
  - `func errcode.Is(err error, code Code) bool`
  - `type process.ProcessSpec struct` fields exactly: `ProcessID string`, `Name string`, `OwnerAgentID string`, `Group string`, `Command string`, `Args []string`, `WorkingDirectory string`, `RunAsUser string`, `Environment map[string]string`, `Instances int`, `Restart process.RestartPolicy`, `Health process.HealthCheckSpec`, `Log process.LogPolicy`, `Resources process.ResourceLimit`, `StartupPriority int`, `Dependencies []process.Dependency`, `Autostart bool`, `StopSignal string`, `StopTimeout time.Duration`, `KillSignal string`, `LatestRevision int64`, `CreatedAt time.Time`, `UpdatedAt time.Time`
  - `type process.RestartPolicy struct { Mode process.RestartMode; MaxRetries int; RetryWindow time.Duration; Backoff process.Backoff }`
  - `type process.Backoff struct { Initial, Max time.Duration; Multiplier float64 }`
  - `type process.RestartMode string` values `never`, `always`, `on-failure`
  - `type process.DesiredState string` values `RUNNING`, `STOPPED`
  - `type process.ObservedState string` values `STOPPED`, `STARTING`, `RUNNING`, `STOPPING`, `EXITED`, `BACKOFF`, `FATAL`, `UNKNOWN`
  - `type process.HealthState string` values `HEALTHY`, `UNHEALTHY`, `UNKNOWN`
  - `type process.Instance struct { InstanceID, ProcessID string; Ordinal int; PID, ShimPID int; Desired DesiredState; Observed ObservedState; Health HealthState; StartedAt, ExitAt *time.Time; ExitCode *int; RestartCount int; ActiveRevision int64; BootID string }`
  - `func process.MakeInstanceID(processID string, ordinal int) string` → `processID + ":" + strconv.Itoa(ordinal)`
  - `func process.ValidateSpec(s ProcessSpec) error`

- [ ] **Step 1: Write the failing validation test**

```go
package process_test

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
)

func TestValidateSpec_RejectsEmptyNameAndZeroInstances(t *testing.T) {
	err := process.ValidateSpec(process.ProcessSpec{Command: "/bin/true"})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestMakeInstanceID(t *testing.T) {
	got := process.MakeInstanceID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", 3)
	if got != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:3" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/process/ -count=1`
Expected: FAIL because packages/files do not exist (or `ValidateSpec` undefined).

- [ ] **Step 3: Write minimal implementation**

`go.mod`:

```text
module github.com/qleelulu/procmesh

go 1.23
```

`internal/errcode/code.go`: define `Code`, `Error` (with `Code`, `Msg`, unwrap `Err`), `E`, `Is`.

`internal/process/types.go`: structs and constants listed above. Defaults when empty: `Instances=1`, `StopSignal=SIGTERM`, `KillSignal=SIGKILL`, `StopTimeout=10s`, `Restart.Mode=on-failure`.

`internal/process/validate.go`: `Name` non-empty, matches `^[a-zA-Z][a-zA-Z0-9_-]{0,62}$`; `Command` non-empty; `Instances >= 1`; `StartupPriority` any int; dependency names unique; `RetryWindow` required if `MaxRetries > 0`; `Backoff.Multiplier >= 1` when set.

`Makefile`:

```makefile
.PHONY: test proto
test:
	go test ./...
proto:
	protoc --go_out=. --go_opt=module=github.com/qleelulu/procmesh proto/shim/v1/shim.proto
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/process/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git init  # only if .git is missing
git add go.mod Makefile internal/version/version.go internal/errcode/code.go internal/process/types.go internal/process/validate.go internal/process/validate_test.go
git commit -m "feat: add module, error codes, and process domain types"
```

---

### Task 2: SQLite store open, schema, node meta

**Files:**
- Create: `internal/store/schema.sql`
- Create: `internal/store/store.go`
- Create: `internal/store/meta.go`
- Create: `internal/store/store_test.go`
- Modify: `go.mod` (add `modernc.org/sqlite`)

**Interfaces:**
- Consumes: `errcode`
- Produces:
  - `func store.Open(path string) (*store.Store, error)`
  - `func (*Store) Close() error`
  - `func (*Store) IntegrityCheck(ctx context.Context) error`
  - `func (*Store) GetOrCreateNodeID(ctx context.Context) (string, error)`
  - `func (*Store) RotateBootID(ctx context.Context) (string, error)`
  - `func (*Store) GetBootID(ctx context.Context) (string, error)`
  - `func (*Store) SetClusterID(ctx context.Context, id string) error`
  - `func (*Store) GetClusterID(ctx context.Context) (string, error)`
  - schema tables: `local_meta(k TEXT PRIMARY KEY, v TEXT NOT NULL)`, plus empty stubs `process_specs`, `process_instances`, `config_revisions`, `operation_journal`, `audit_events` (full columns in later tasks; create the tables now so migrate is one file)

- [ ] **Step 1: Write the failing test**

```go
package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/qleelulu/procmesh/internal/store"
)

func TestOpen_CreatesFileAndStableNodeID(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id1, err := s.GetOrCreateNodeID(ctx)
	if err != nil || id1 == "" {
		t.Fatalf("id1 %q err %v", id1, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	id2, err := s2.GetOrCreateNodeID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("node_id changed: %s vs %s", id1, id2)
	}
	boot1, err := s2.RotateBootID(ctx)
	if err != nil || boot1 == "" {
		t.Fatalf("boot %q err %v", boot1, err)
	}
	boot2, err := s2.RotateBootID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if boot1 == boot2 {
		t.Fatal("boot_id must change every rotate")
	}
}

func TestIntegrityCheck_OKOnFreshDB(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.IntegrityCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -count=1`
Expected: FAIL, `store.Open` undefined.

- [ ] **Step 3: Write minimal implementation**

```bash
go get modernc.org/sqlite
```

`schema.sql` embed with `//go:embed schema.sql`. Apply on open with `CREATE TABLE IF NOT EXISTS`. Use DSN `file:PATH?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)`. `GetOrCreateNodeID` inserts UUID if missing. `RotateBootID` always writes a new UUID. `IntegrityCheck` runs `PRAGMA integrity_check` and returns `errcode.E(errcode.DEGRADED, msg)` if not `ok`.

`local_meta` keys: `schema_version`, `node_id`, `boot_id`, `cluster_id`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "feat: add sqlite store open, schema, and node identity"
```

---

### Task 3: Spec persist and revision CAS

**Files:**
- Create: `internal/store/spec.go`
- Create: `internal/store/spec_test.go`
- Modify: `internal/store/schema.sql` (full `process_specs` and `config_revisions` columns)

**Interfaces:**
- Consumes: `process.ProcessSpec`, `errcode`
- Produces:
  - `func (*Store) PutSpec(ctx context.Context, spec process.ProcessSpec, expectedRevision int64, operator, comment string) (process.ProcessSpec, error)`
  - `func (*Store) GetSpec(ctx context.Context, processID string) (process.ProcessSpec, error)`
  - `func (*Store) GetSpecByName(ctx context.Context, name string) (process.ProcessSpec, error)`
  - `func (*Store) ListSpecs(ctx context.Context) ([]process.ProcessSpec, error)`
  - `func (*Store) DeleteSpec(ctx context.Context, processID string, expectedRevision int64) error`
  - `func (*Store) ListRevisions(ctx context.Context, processID string) ([]store.Revision, error)`
  - `type store.Revision struct { Revision int64; Operator string; Timestamp time.Time; Diff string; Comment string; SpecJSON []byte }`
  - `func (*Store) RollbackSpec(ctx context.Context, processID string, toRevision, expectedLatest int64, operator, comment string) (process.ProcessSpec, error)`
  - Create: `expectedRevision == 0` and name must not exist; success sets `LatestRevision=1`
  - Update: `expectedRevision` must equal current `LatestRevision` or return `CONFLICT`; new revision = old+1
  - Rollback: copy `toRevision` payload into a new revision; do not delete old rows

- [ ] **Step 1: Write the failing test**

```go
package store_test

func TestPutSpec_CASConflict(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "nginx", Command: "/usr/sbin/nginx", Instances: 1}
	got, err := s.PutSpec(ctx, spec, 0, "admin", "create")
	if err != nil || got.LatestRevision != 1 {
		t.Fatalf("create: %+v %v", got, err)
	}
	spec.Command = "/bin/nginx"
	if _, err := s.PutSpec(ctx, spec, 1, "admin", "ok"); err != nil {
		t.Fatal(err)
	}
	spec.Command = "/opt/nginx"
	_, err = s.PutSpec(ctx, spec, 1, "admin", "stale")
	if !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("want CONFLICT got %v", err)
	}
	cur, err := s.GetSpec(ctx, "p1")
	if err != nil || cur.Command != "/bin/nginx" || cur.LatestRevision != 2 {
		t.Fatalf("lost update: %+v %v", cur, err)
	}
}

func TestRollbackSpec_CreatesNewRevision(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	spec := process.ProcessSpec{ProcessID: "p1", Name: "api", Command: "v1", Instances: 1}
	if _, err := s.PutSpec(ctx, spec, 0, "a", ""); err != nil {
		t.Fatal(err)
	}
	spec.Command = "v2"
	if _, err := s.PutSpec(ctx, spec, 1, "a", ""); err != nil {
		t.Fatal(err)
	}
	rolled, err := s.RollbackSpec(ctx, "p1", 1, 2, "a", "undo")
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Command != "v1" || rolled.LatestRevision != 3 {
		t.Fatalf("got %+v", rolled)
	}
	revs, err := s.ListRevisions(ctx, "p1")
	if err != nil || len(revs) != 3 {
		t.Fatalf("revs=%d err=%v", len(revs), err)
	}
}
```

Put `openStore` helper in `spec_test.go`: Open temp db.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -count=1 -run 'TestPutSpec_CASConflict|TestRollbackSpec'`
Expected: FAIL, methods undefined.

- [ ] **Step 3: Write minimal implementation**

Serialize spec to JSON in `process_specs.spec_json` plus indexed columns `process_id`, `name` (UNIQUE), `latest_revision`. Each `PutSpec`/`RollbackSpec` inserts `config_revisions(process_id, revision, operator, ts, diff, comment, spec_json)` in the same transaction. Diff can be a unified text of command/args/env only for P0.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat: persist process specs with revision CAS and rollback"
```

---

### Task 4: Instances and idempotent operation journal

**Files:**
- Create: `internal/store/instance.go`
- Create: `internal/store/journal.go`
- Create: `internal/store/runtime_test.go`

**Interfaces:**
- Consumes: `process.Instance`, `errcode`
- Produces:
  - `func (*Store) PutInstance(ctx context.Context, inst process.Instance) error`
  - `func (*Store) GetInstance(ctx context.Context, instanceID string) (process.Instance, error)`
  - `func (*Store) ListInstances(ctx context.Context, processID string) ([]process.Instance, error)`
  - `type store.Operation struct { OperationID, Operator, SourceAgent, Target, Type string; RequestPayload []byte; CreatedAt, StartedAt, FinishedAt time.Time; Status string; Result []byte; Error string }`
  - Operation status strings: `PENDING`, `RUNNING`, `SUCCESS`, `FAILED`, `TIMEOUT`, `UNKNOWN`
  - `func (*Store) BeginOperation(ctx context.Context, op store.Operation) (existing store.Operation, duplicate bool, err error)` — if `operation_id` exists, return the stored row and `duplicate=true` without modifying it
  - `func (*Store) FinishOperation(ctx context.Context, operationID, status string, result []byte, errMsg string) error`
  - `func (*Store) GetOperation(ctx context.Context, operationID string) (store.Operation, error)`

- [ ] **Step 1: Write the failing test**

```go
func TestBeginOperation_Idempotent(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	op := store.Operation{OperationID: "op-1", Type: "start", Target: "p1", Status: "PENDING"}
	if _, dup, err := s.BeginOperation(ctx, op); err != nil || dup {
		t.Fatalf("first: dup=%v err=%v", dup, err)
	}
	if err := s.FinishOperation(ctx, "op-1", "SUCCESS", []byte(`{"ok":true}`), ""); err != nil {
		t.Fatal(err)
	}
	got, dup, err := s.BeginOperation(ctx, op)
	if err != nil || !dup || got.Status != "SUCCESS" {
		t.Fatalf("second: %+v dup=%v err=%v", got, dup, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -count=1 -run TestBeginOperation_Idempotent`
Expected: FAIL, undefined.

- [ ] **Step 3: Write minimal implementation**

`operation_journal.operation_id` PRIMARY KEY. `BeginOperation` is `INSERT OR IGNORE` then `SELECT`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat: add instance rows and idempotent operation journal"
```

---

### Task 5: Append-only local audit

**Files:**
- Create: `internal/store/audit.go`
- Create: `internal/store/audit_test.go`

**Interfaces:**
- Consumes: `errcode`
- Produces:
  - `type store.AuditEvent struct { AuditID string; Timestamp time.Time; UserID, Username, SourceIP, SourceAgent, TargetAgent, Resource, Action, OperationID, Result string; Metadata []byte }`
  - `func (*Store) AppendAudit(ctx context.Context, ev store.AuditEvent) error` — generates `AuditID` UUID if empty; never updates
  - `func (*Store) ListAudit(ctx context.Context, resource string, limit int) ([]store.AuditEvent, error)` — newest first

- [ ] **Step 1: Write the failing test**

```go
func TestAppendAudit_IsAppendOnly(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.AppendAudit(ctx, store.AuditEvent{Action: "process.start", Resource: "nginx", Result: "SUCCESS", OperationID: "op-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(ctx, store.AuditEvent{Action: "process.stop", Resource: "nginx", Result: "SUCCESS", OperationID: "op-2"}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListAudit(ctx, "nginx", 10)
	if err != nil || len(evs) != 2 || evs[0].Action != "process.stop" {
		t.Fatalf("%+v %v", evs, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -count=1 -run TestAppendAudit`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

Table `audit_events` with no UPDATE trigger needed; just never expose an update API.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat: add append-only local audit log"
```

---

### Task 6: Observed-state machine

**Files:**
- Create: `internal/process/machine.go`
- Create: `internal/process/machine_test.go`

**Interfaces:**
- Consumes: `ObservedState`
- Produces:
  - `type process.Event string` values: `EvStart`, `EvStartOK`, `EvStartFail`, `EvExit`, `EvStop`, `EvStopped`, `EvRetry`, `EvRetriesExhausted`, `EvLost`
  - `func process.ApplyObserved(cur ObservedState, ev Event) (ObservedState, error)` — illegal pair returns `errcode.INVALID`
  - Legal transitions only:
    - `STOPPED + EvStart → STARTING`
    - `STARTING + EvStartOK → RUNNING`
    - `STARTING + EvStartFail → BACKOFF`
    - `RUNNING + EvExit → EXITED`
    - `RUNNING + EvStop → STOPPING`
    - `STOPPING + EvStopped → STOPPED`
    - `EXITED + EvRetry → STARTING`
    - `EXITED + EvRetriesExhausted → FATAL`
    - `BACKOFF + EvRetry → STARTING`
    - `BACKOFF + EvRetriesExhausted → FATAL`
    - `FATAL + EvStart → STARTING` (after operator reset, manager sends EvStart)
    - any + `EvLost → UNKNOWN`
    - `UNKNOWN + EvStartOK → RUNNING`
    - `UNKNOWN + EvStopped → STOPPED`

- [ ] **Step 1: Write the failing test**

```go
func TestApplyObserved_HappyPathAndIllegal(t *testing.T) {
	s, err := process.ApplyObserved(process.ObservedStopped, process.EvStart)
	if err != nil || s != process.ObservedStarting {
		t.Fatalf("%s %v", s, err)
	}
	s, err = process.ApplyObserved(process.ObservedStarting, process.EvStartOK)
	if err != nil || s != process.ObservedRunning {
		t.Fatalf("%s %v", s, err)
	}
	_, err = process.ApplyObserved(process.ObservedStopped, process.EvStop)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
}
```

Name the constants `ObservedStopped` etc. as `const ObservedStopped ObservedState = "STOPPED"` in `types.go` if not already.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/process/ -count=1 -run TestApplyObserved`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

Table-driven map `[2]string{cur, ev} → next`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/process/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/process
git commit -m "feat: add process observed-state machine"
```

---

### Task 7: Restart policy and backoff

**Files:**
- Create: `internal/process/restart.go`
- Create: `internal/process/restart_test.go`

**Interfaces:**
- Consumes: `RestartPolicy`, `DesiredState`, `ObservedState`
- Produces:
  - `type process.RestartDecision struct { Restart bool; Fatal bool; Delay time.Duration }`
  - `func process.DecideRestart(pol RestartPolicy, desired DesiredState, observed ObservedState, exitCode int, failures []time.Time, now time.Time) process.RestartDecision`
  - Rules:
    - `desired == STOPPED` → no restart
    - `observed` not in `{EXITED, BACKOFF}` → no restart
    - `Mode == never` → no restart
    - `Mode == on-failure` and `exitCode == 0` → no restart
    - Count `failures` inside `RetryWindow`; if `len >= MaxRetries` and `MaxRetries > 0` → `Fatal: true`
    - Else `Restart: true` and `Delay = min(Max, Initial * Multiplier^n)` where `n` is failure count in window (`Initial` default 1s, `Max` default 60s, `Multiplier` default 2)

- [ ] **Step 1: Write the failing test**

```go
func TestDecideRestart_CrashLoopBecomesFatal(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pol := process.RestartPolicy{Mode: process.RestartOnFailure, MaxRetries: 3, RetryWindow: time.Minute, Backoff: process.Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 2}}
	fails := []time.Time{now.Add(-3 * time.Second), now.Add(-2 * time.Second), now.Add(-1 * time.Second)}
	d := process.DecideRestart(pol, process.DesiredRunning, process.ObservedExited, 1, fails, now)
	if !d.Fatal || d.Restart {
		t.Fatalf("%+v", d)
	}
}

func TestDecideRestart_OnFailureIgnoresCleanExit(t *testing.T) {
	d := process.DecideRestart(process.RestartPolicy{Mode: process.RestartOnFailure}, process.DesiredRunning, process.ObservedExited, 0, nil, time.Now())
	if d.Restart || d.Fatal {
		t.Fatalf("%+v", d)
	}
}
```

Define `DesiredRunning DesiredState = "RUNNING"`, `ObservedExited = "EXITED"`, `RestartOnFailure = "on-failure"` in `types.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/process/ -count=1 -run TestDecideRestart`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

Pure function, no time.Now inside.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/process/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/process
git commit -m "feat: add restart policy and crash-loop fatal decision"
```

---

### Task 8: Dependency DAG and startup order

**Files:**
- Create: `internal/process/deps.go`
- Create: `internal/process/deps_test.go`

**Interfaces:**
- Consumes: `ProcessSpec.Dependencies`, `StartupPriority`
- Produces:
  - `type process.DepCondition string` values `STARTED`, `HEALTHY`
  - `func process.StartupOrder(specs []ProcessSpec) ([]string, error)` — returns process IDs. Cycle → `errcode.INVALID` message `circular dependency`. Missing dep name → `errcode.INVALID`. Among ready nodes, sort by `StartupPriority` ascending then `Name` ascending. Edges: each spec depends on the spec whose `Name` matches `Dependency.ProcessName`.

- [ ] **Step 1: Write the failing test**

```go
func TestStartupOrder_PriorityAndCycle(t *testing.T) {
	specs := []process.ProcessSpec{
		{ProcessID: "api", Name: "api", StartupPriority: 30, Dependencies: []process.Dependency{{ProcessName: "mysql", Condition: process.DepHealthy}}},
		{ProcessID: "mysql", Name: "mysql", StartupPriority: 10},
		{ProcessID: "redis", Name: "redis", StartupPriority: 20},
	}
	got, err := process.StartupOrder(specs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mysql", "redis", "api"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	cycle := []process.ProcessSpec{
		{ProcessID: "a", Name: "a", Dependencies: []process.Dependency{{ProcessName: "b"}}},
		{ProcessID: "b", Name: "b", Dependencies: []process.Dependency{{ProcessName: "a"}}},
	}
	if _, err := process.StartupOrder(cycle); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/process/ -count=1 -run TestStartupOrder`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

Kahn topological sort. Default condition if empty: `HEALTHY`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/process/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/process
git commit -m "feat: add local process dependency DAG ordering"
```

---

### Task 9: Shim protobuf and length-prefixed frames

**Files:**
- Create: `proto/shim/v1/shim.proto`
- Create: `internal/shim/frame.go`
- Create: `internal/shim/frame_test.go`
- Modify: `Makefile` proto target if needed

**Interfaces:**
- Consumes: nothing
- Produces:
  - proto package `procmesh.shim.v1`, go_package `github.com/qleelulu/procmesh/proto/shim/v1;shimpb`
  - messages: `StartRequest` (command string, args repeated string, env map<string,string>, cwd string, run_as_user string, stdout_path string, stderr_path string), `StartResponse` (pid int32), `StopRequest` (signal string, timeout_ms int32, kill_signal string), `StopResponse` (exit_code int32), `SignalRequest` (signal string), `SignalResponse`, `StatusRequest`, `StatusResponse` (pid int32, alive bool, exit_code int32, started_unix int64), `WaitRequest`, `WaitResponse` (exit_code int32)
  - `func shim.WriteFrame(w io.Writer, payload []byte) error` — 4-byte big-endian length + payload; reject `len > 16<<20`
  - `func shim.ReadFrame(r io.Reader) ([]byte, error)`

- [ ] **Step 1: Write the failing frame test**

```go
func TestFrame_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := shim.WriteFrame(&buf, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := shim.ReadFrame(&buf)
	if err != nil || string(got) != "hello" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestFrame_RejectsTooLarge(t *testing.T) {
	var buf bytes.Buffer
	if err := shim.WriteFrame(&buf, make([]byte, 16<<20+1)); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/shim/ -count=1`
Expected: FAIL

- [ ] **Step 3: Write proto + implementation**

```protobuf
syntax = "proto3";
package procmesh.shim.v1;
option go_package = "github.com/qleelulu/procmesh/proto/shim/v1;shimpb";

message StartRequest {
  string command = 1;
  repeated string args = 2;
  map<string, string> env = 3;
  string cwd = 4;
  string run_as_user = 5;
  string stdout_path = 6;
  string stderr_path = 7;
}
message StartResponse { int32 pid = 1; string error = 2; }
message StopRequest { string signal = 1; int32 timeout_ms = 2; string kill_signal = 3; }
message StopResponse { int32 exit_code = 1; string error = 2; }
message SignalRequest { string signal = 1; }
message SignalResponse { string error = 1; }
message StatusRequest {}
message StatusResponse {
  int32 pid = 1;
  bool alive = 2;
  int32 exit_code = 3;
  int64 started_unix = 4;
  string error = 5;
}
message WaitRequest {}
message WaitResponse { int32 exit_code = 1; string error = 2; }

message Envelope {
  oneof body {
    StartRequest start = 1;
    StopRequest stop = 2;
    SignalRequest signal = 3;
    StatusRequest status = 4;
    WaitRequest wait = 5;
    StartResponse start_ok = 10;
    StopResponse stop_ok = 11;
    SignalResponse signal_ok = 12;
    StatusResponse status_ok = 13;
    WaitResponse wait_ok = 14;
  }
}
```

Generate with `make proto`. If `protoc` is missing, commit a checked-in `proto/shim/v1/shim.pb.go` generated on a machine that has it — do not hand-write pb.go.

Each request/response is a marshaled `Envelope` inside a frame.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/shim/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add proto/shim/v1 internal/shim Makefile
git commit -m "feat: add shim protobuf envelope and frame codec"
```

---

### Task 10: Shim server binary

**Files:**
- Create: `internal/shim/server.go`
- Create: `internal/shim/server_test.go`
- Create: `cmd/procmesh-shim/main.go`

**Interfaces:**
- Consumes: `shim.ReadFrame`, `WriteFrame`, `shimpb.Envelope`
- Produces:
  - `func shim.Serve(ctx context.Context, socketPath string) error` — listen unix, accept one client at a time (or sequential conns), handle envelopes until ctx cancel
  - `cmd/procmesh-shim` flags: `--socket` (required), `--instance-id` (required)
  - On start: `unix.Setsid()` via `golang.org/x/sys/unix` (Linux/macOS). If Setsid fails, log and continue (already session leader).
  - `Start`: if already has a live child, return error string `already started`. Else `exec.Command`, set `SysProcAttr.Setsid: true` on the child too, redirect stdout/stderr to given paths (`O_CREATE|O_APPEND|O_WRONLY`, 0640). Do not use shell.
  - `run_as_user` empty: current user. Non-empty: lookup uid/gid and set `Credential`; failure returns error in response (no process started).
  - `Stop`: signal child with `signal` (default SIGTERM), wait `timeout_ms` (default 10000), then `kill_signal` (default SIGKILL), then wait and return exit code.
  - `Status`: `alive` from `child.ProcessState == nil` plus kill(pid,0).
  - `Wait`: block until child exits or ctx done.
  - Never dial the network. Never read SQLite.

- [ ] **Step 1: Write the failing integration test**

```go
func TestServe_StartAndStopTrue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	errCh := make(chan error, 1)
	go func() { errCh <- shim.Serve(ctx, sock) }()
	waitSock(t, sock)
	c, err := shim.Dial(sock) // Dial added in this task as unexported helper or in client.go stub
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out := filepath.Join(dir, "out.log")
	resp, err := c.Start(ctx, &shimpb.StartRequest{Command: "/bin/sleep", Args: []string{"30"}, StdoutPath: out, StderrPath: out})
	if err != nil || resp.GetPid() <= 0 {
		t.Fatalf("%v %+v", err, resp)
	}
	st, err := c.Status(ctx)
	if err != nil || !st.GetAlive() {
		t.Fatalf("%v %+v", err, st)
	}
	if _, err := c.Stop(ctx, &shimpb.StopRequest{Signal: "SIGTERM", TimeoutMs: 2000, KillSignal: "SIGKILL"}); err != nil {
		t.Fatal(err)
	}
}
```

If `Dial` is Task 11, put a tiny test-only dial in `server_test.go` that writes/reads envelopes. Prefer introducing `client.go` Dial here so the test is readable:

```go
func Dial(socketPath string) (*Client, error)
func (*Client) Close() error
func (*Client) Start(ctx, *shimpb.StartRequest) (*shimpb.StartResponse, error)
func (*Client) Stop(ctx, *shimpb.StopRequest) (*shimpb.StopResponse, error)
func (*Client) Status(ctx) (*shimpb.StatusResponse, error)
func (*Client) Signal(ctx, *shimpb.SignalRequest) (*shimpb.SignalResponse, error)
func (*Client) Wait(ctx) (*shimpb.WaitResponse, error)
```

Implement `Client` in this task if needed so the test compiles; Task 11 then adds launch/discover.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/shim/ -count=1 -run TestServe_StartAndStopTrue`
Expected: FAIL

- [ ] **Step 3: Write Serve + cmd/procmesh-shim**

`main.go` only parses flags and `shim.Serve(context.Background(), socket)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/shim/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/shim cmd/procmesh-shim
git commit -m "feat: add procmesh-shim server that execs and signals children"
```

---

### Task 11: Launch shim as sibling process and reconnect

**Files:**
- Create: `internal/shim/launch.go`
- Create: `internal/shim/launch_test.go`
- Create: `internal/paths/paths.go`
- Create: `internal/paths/paths_test.go`

**Interfaces:**
- Consumes: `shim.Dial`, `shim.Client`
- Produces:
  - `type paths.Layout struct { Root, Store, ShimDir, LogDir, RuntimeDir, ClusterDir string }`
  - `func paths.New(root string) paths.Layout` — `store.db`, `shim/`, `logs/`, `runtime/`, `cluster/`
  - `func (Layout) ShimSocket(instanceID string) string` — `shim/<sanitized-instance-id>.sock` (`:` → `_`)
  - `func (Layout) Ensure() error` — mkdir 0750
  - `func shim.LookPath() (string, error)` — `exec.LookPath("procmesh-shim")` then `filepath.Join(filepath.Dir(os.Args[0]), "procmesh-shim")`
  - `func shim.Launch(ctx context.Context, bin, socket, instanceID string) (shimPID int, err error)` — start shim with `SysProcAttr.Setsid: true`, extra files closed, stdout/stderr to `socket+".shim.log"`
  - `func shim.Discover(dir string) (map[string]string, error)` — instanceID(sanitized) → socket path for `*.sock`
  - `func shim.Reconnect(ctx context.Context, socket string) (*Client, *shimpb.StatusResponse, error)`

- [ ] **Step 1: Write the failing test**

```go
func TestLaunch_ThenReconnectAfterClientDrop(t *testing.T) {
	bin, err := exec.LookPath("procmesh-shim")
	if err != nil {
		t.Skip("build shim first: go build -o $PWD/procmesh-shim ./cmd/procmesh-shim and add to PATH")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "i1.sock")
	if _, err := shim.Launch(context.Background(), bin, sock, "p:1"); err != nil {
		t.Fatal(err)
	}
	c1, st, err := shim.Reconnect(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = st
	c1.Close()
	c2, _, err := shim.Reconnect(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	c2.Close()
}
```

Also add a unit test for `paths.ShimSocket` that does not need the binary:

```go
func TestShimSocket_SanitizesColon(t *testing.T) {
	l := paths.New("/data")
	if l.ShimSocket("abc:0") != "/data/shim/abc_0.sock" {
		t.Fatal(l.ShimSocket("abc:0"))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/paths/ ./internal/shim/ -count=1 -run 'TestLaunch|TestShimSocket'`
Expected: FAIL (or skip+fail on paths)

- [ ] **Step 3: Write implementation**

Document in comment: tests that need the binary should `go build -o testdata/procmesh-shim ./cmd/procmesh-shim` in `TestMain` of `launch_test.go` and pass that path to `Launch`. Prefer TestMain build over Skip so CI is deterministic:

```go
func TestMain(m *testing.M) {
	os.Exit(run(m))
}
func run(m *testing.M) int {
	dir, _ := os.MkdirTemp("", "shim-bin")
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "procmesh-shim")
	cmd := exec.Command("go", "build", "-o", bin, "../../../cmd/procmesh-shim")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build shim: %v\n%s", err, out)
		return 1
	}
	testShimBin = bin
	return m.Run()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/shim/ ./internal/paths/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/shim internal/paths
git commit -m "feat: launch and reconnect procmesh-shim over unix sockets"
```

---

### Task 12: Process manager reconcile and recover

**Files:**
- Create: `internal/process/manager.go`
- Create: `internal/process/manager_test.go`
- Create: `internal/process/recover_test.go`

**Interfaces:**
- Consumes: `store.Store`, `shim.Launch/Reconnect/Discover/LookPath/Client`, `paths.Layout`, state machine, restart, deps
- Produces:
  - `type process.Deps struct { Store *store.Store; Layout paths.Layout; ShimBin string; Now func() time.Time; LookUser func(user string) error; Logs *logmgr.Manager }`
  - `func process.NewManager(d Deps) *Manager`
  - `func (*Manager) ApplySpec(ctx context.Context, spec ProcessSpec, expectedRevision int64, opID, operator, comment string) (ProcessSpec, error)`
  - `func (*Manager) DeleteSpec(ctx context.Context, processID string, expectedRevision int64, opID, operator string) error` — must be `desired STOPPED` and all instances `STOPPED`/`FATAL`/`UNKNOWN` with no live PID, else `INVALID`
  - `func (*Manager) SetDesired(ctx context.Context, processID string, desired DesiredState, opID, operator string) error`
  - `func (*Manager) ResetFailure(ctx context.Context, processID, opID, operator string) error` — clears failure timestamps, `FATAL → STOPPED` then if desired RUNNING, next Reconcile starts
  - `func (*Manager) Adopt(ctx context.Context, instanceID string, pid int, opID, operator string) error` — `kill(pid,0)` must succeed; record PID; launch shim is NOT done in P0 Adopt (attach-to-existing is: store PID, mark RUNNING/UNKNOWN health, write runtime file). If pid dead → `NOT_FOUND`
  - `func (*Manager) Recover(ctx context.Context) error` — load specs/instances; discover sockets; reconnect; never start a second copy of a live pid
  - `func (*Manager) Reconcile(ctx context.Context) error` — one pass in `StartupOrder`; start/stop to match desired; apply DecideRestart
  - Recover rules:
    1. Socket reconnect OK and child alive → take over, do not Launch
    2. No socket, runtime file has PID, `kill(pid,0)` OK → `Observed=UNKNOWN`, audit `ORPHAN_PROCESS`, do not start another
    3. No socket, no live PID → if desired RUNNING and Autostart (on Recover after host reboot treat Autostart && desired RUNNING) start; if Agent crash recover, desired RUNNING always tries to recreate shim only when PID is dead
    4. Host reboot vs Agent crash: compare stored instance `BootID` to current store boot id. Different boot id → old PIDs are invalid even if a reused PID exists. Always ignore PIDs when boot id mismatches.

- [ ] **Step 1: Write the failing recover test**

```go
func TestRecover_DoesNotDoubleStartLiveShim(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	spec := process.ProcessSpec{ProcessID: "p1", Name: "sleep", Command: "/bin/sleep", Args: []string{"60"}, Instances: 1, Autostart: true}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-create", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-start", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err := st.ListInstances(ctx, "p1")
	if err != nil || len(insts) != 1 || insts[0].PID <= 0 {
		t.Fatalf("%+v %v", insts, err)
	}
	pid1 := insts[0].PID
	m2 := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if err := m2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	insts, err = st.ListInstances(ctx, "p1")
	if err != nil || insts[0].PID != pid1 {
		t.Fatalf("double start? %+v %v", insts, err)
	}
	if err := unix.Kill(pid1, 0); err != nil {
		t.Fatalf("child died: %v", err)
	}
}
```

Add `internal/process/shimbin_test.go`:

```go
package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var testShimBin string

func TestMain(m *testing.M) {
	os.Exit(runProcessTests(m))
}

func runProcessTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "shim-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "procmesh-shim")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/procmesh-shim")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build shim: %v\n%s", err, out)
		return 1
	}
	testShimBin = bin
	return m.Run()
}
```

Second test:

```go
func TestRecover_OrphanPIDBecomesUnknown(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	spec := process.ProcessSpec{ProcessID: "p1", Name: "self", Command: "/bin/true", Instances: 1}
	if _, err := st.PutSpec(ctx, spec, 0, "t", ""); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	inst := process.Instance{
		InstanceID: process.MakeInstanceID("p1", 0),
		ProcessID:  "p1",
		Ordinal:    0,
		PID:        pid,
		Desired:    process.DesiredRunning,
		Observed:   process.ObservedRunning,
		BootID:     mustBoot(t, st),
	}
	if err := st.PutInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now})
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observed != process.ObservedUnknown {
		t.Fatalf("got %s", got.Observed)
	}
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("recover must not kill orphan: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/process/ -count=1 -run TestRecover`
Expected: FAIL

- [ ] **Step 3: Write Manager**

Keep `manager.go` under 400 lines. Split helpers into `internal/process/runtimefile.go` if needed (`read/write runtime/<instance>.json` with pid, shim_pid, boot_id).

`ApplySpec` validates, `PutSpec`, ensures instance rows 0..instances-1, journals the op, audits `process.create` or `process.update`.

`SetDesired` updates all instances' desired, journals, audits.

`Reconcile` is synchronous and single-threaded (mutex on Manager).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/process/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/process
git commit -m "feat: add process manager recover and reconcile without double-start"
```

---

### Task 13: File logs and disk protection

**Files:**
- Create: `internal/logmgr/logmgr.go`
- Create: `internal/logmgr/logmgr_test.go`

**Interfaces:**
- Consumes: `process.LogPolicy`, `paths.Layout`
- Produces:
  - `type process.LogPolicy struct { MaxSize int64; MaxFiles int; MaxAge time.Duration; Compress bool }` — put this on `types.go` if missing. Defaults: 100<<20, 10, 7*24h, true
  - `func logmgr.InstancePaths(layout paths.Layout, processID, instanceID string) (stdout, stderr string)`
  - `func logmgr.Prepare(paths ...string) error` — mkdir parent, create empty files
  - `type logmgr.DiskUsage func(root string) (usedPercent float64, err error)`
  - `type logmgr.Manager struct { Root string; Usage DiskUsage; Now func() time.Time }`
  - `func (*Manager) Protect(ctx context.Context) (level logmgr.Level, err error)`
  - `type logmgr.Level int` (`OK=0`, `Warn=1` used>85, `Cleanup=2` used>90, `Emergency=3` used>95)
  - `Cleanup`: delete oldest files under `logs/` matching `*.log` / `*.log.gz` until used ≤ 85 or no more log files. Never delete `store.db`, `raft/`, `cluster/`, `runtime/`.
  - `Emergency`: same deletions; `func (*Manager) WritesAllowed() bool` is false until used ≤ 90
  - `func Tail(path string, maxLines int) ([]string, error)` — last N lines, default max 1000, hard cap 10000

- [ ] **Step 1: Write the failing test**

```go
func TestProtect_EmergencyStopsWrites(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs", "p", "i")
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(logDir, "stdout.log")
	if err := os.WriteFile(old, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	m := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return 96, nil }, Now: time.Now}
	lvl, err := m.Protect(context.Background())
	if err != nil || lvl != logmgr.Emergency || m.WritesAllowed() {
		t.Fatalf("lvl=%v allowed=%v err=%v", lvl, m.WritesAllowed(), err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expected old log removed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logmgr/ -count=1`
Expected: FAIL

- [ ] **Step 3: Write implementation**

On real Linux, default `Usage` reads the mount of `Root` via `golang.org/x/sys/unix.Statfs`. Tests inject `Usage`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/logmgr/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/logmgr internal/process/types.go
git commit -m "feat: add log paths, tail, and disk protection levels"
```

Wire `logmgr.Prepare` into Manager start path in the same commit: when launching, pass stdout/stderr paths from `InstancePaths`. If `WritesAllowed()==false`, still start the process but open `/dev/null` for stdio and audit `LOG_WRITES_DISABLED`.

---

### Task 14: Health checks

**Files:**
- Create: `internal/health/health.go`
- Create: `internal/health/health_test.go`
- Modify: `internal/process/types.go` (`HealthCheckSpec`)
- Modify: `internal/process/manager.go` (apply health results; do not change desired)

**Interfaces:**
- Consumes: `process.HealthCheckSpec`, instance PID/ports
- Produces:
  - `type process.HealthCheckSpec struct { Type string; URL string; Method string; ExpectedStatus int; Address string; Command string; Args []string; InitialDelay, Interval, Timeout time.Duration; FailureThreshold, SuccessThreshold int; RestartOnFailure bool; RestartCooldown time.Duration }`
  - `Type`: `""`/`alive` (kill(pid,0)), `http`, `tcp`, `exec`
  - `func health.Check(ctx context.Context, spec process.HealthCheckSpec, pid int) error` — nil means healthy
  - `type health.Tracker struct` with `func (*Tracker) Observe(err error, now time.Time) process.HealthState` applying failure/success thresholds
  - Manager: after instance RUNNING and `now >= started+InitialDelay`, call Check on Interval. On UNHEALTHY and `RestartOnFailure` and cooldown elapsed, stop+start (counts as restart toward crash loop). Tracker state in memory, rebuilt on Recover as `UNKNOWN` until first checks.

- [ ] **Step 1: Write the failing test**

```go
func TestCheck_HTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	err := health.Check(context.Background(), process.HealthCheckSpec{Type: "http", URL: srv.URL, ExpectedStatus: 200, Timeout: time.Second}, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = health.Check(context.Background(), process.HealthCheckSpec{Type: "http", URL: srv.URL, ExpectedStatus: 204, Timeout: time.Second}, 0)
	if err == nil {
		t.Fatal("expected status mismatch")
	}
}

func TestTracker_Thresholds(t *testing.T) {
	tr := health.NewTracker(process.HealthCheckSpec{FailureThreshold: 3, SuccessThreshold: 2})
	now := time.Now()
	if tr.Observe(errors.New("x"), now) != process.HealthUnknown {
		t.Fatal()
	}
	if tr.Observe(errors.New("x"), now) != process.HealthUnknown {
		t.Fatal()
	}
	if tr.Observe(errors.New("x"), now) != process.HealthUnhealthy {
		t.Fatal()
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/health/ -count=1`
Expected: FAIL

- [ ] **Step 3: Write implementation**

HTTP client timeout from spec; no redirects beyond 5; only http/https. TCP dials `Address`. Exec uses `exec.CommandContext` with Timeout, not a shell.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/health/ ./internal/process/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/health internal/process
git commit -m "feat: add alive/http/tcp/exec health checks"
```

---

### Task 15: Agent process + loopback JSON control

**Files:**
- Create: `cmd/procmesh-agent/main.go`
- Create: `internal/localhttp/server.go`
- Create: `internal/localhttp/server_test.go`
- Create: `internal/agent/run.go`

**Interfaces:**
- Consumes: `process.Manager`, `store.Store`, `paths.Layout`, `logmgr.Manager`
- Produces:
  - `procmesh-agent --data-dir DIR --listen 127.0.0.1:9000 --shim-bin PATH`
  - On start: `Open` store; if IntegrityCheck fails, process manager still constructed but HTTP `/readyz` → 503 body `DEGRADED`; **do not** stop or signal any discovered live processes
  - `RotateBootID` once per start
  - `Recover` then a 1s ticker `Reconcile` + `Protect`
  - Loopback JSON (no auth in P0; bind default `127.0.0.1:9000` only — refuse listen on non-loopback unless `--insecure-listen` which still logs a warning):
    - `GET /healthz` → 200 `ok`
    - `GET /readyz` → 200 `ok` or 503 `DEGRADED`
    - `GET /v1/processes` → list specs + instances
    - `POST /v1/processes` body `{"operation_id","operator","expected_revision","spec":{...}}`
    - `PUT /v1/processes/{id}` same body
    - `POST /v1/processes/{id}/start` body `{"operation_id","operator"}`
    - `POST /v1/processes/{id}/stop`
    - `POST /v1/processes/{id}/restart` (stop then desired RUNNING; same op id recorded once)
    - `POST /v1/processes/{id}/reset-failure`
    - `POST /v1/instances/{id}/adopt` body `{"operation_id","operator","pid"}`
    - `GET /v1/processes/{id}/logs?lines=100` → stdout tail
  - Duplicate `operation_id`: return stored result with HTTP 200 and header `X-Idempotent-Replay: 1`
  - CAS conflict: HTTP 409 JSON `{"code":"CONFLICT","message":"..."}`

- [ ] **Step 1: Write the failing test**

```go
func TestLocalHTTP_CreateStartAndConflict(t *testing.T) {
	srv := startTestAgent(t)
	body := `{"operation_id":"op-c","operator":"t","expected_revision":0,"spec":{"process_id":"p1","name":"true","command":"/bin/sleep","args":["5"],"instances":1}}`
	res, err := http.Post(srv+"/v1/processes", "application/json", strings.NewReader(body))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("%v %v", err, res)
	}
	start := `{"operation_id":"op-s","operator":"t"}`
	res, err = http.Post(srv+"/v1/processes/p1/start", "application/json", strings.NewReader(start))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("start %v %v", err, res)
	}
	res, err = http.Post(srv+"/v1/processes", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 409 {
		t.Fatalf("want 409 got %d", res.StatusCode)
	}
}
```

`startTestAgent` opens temp data-dir, listens `127.0.0.1:0`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/localhttp/ -count=1`
Expected: FAIL

- [ ] **Step 3: Write implementation**

`internal/agent/run.go` owns lifecycle. `cmd/procmesh-agent/main.go` only flags + `agent.Run`. JSON field names snake_case matching struct tags `json:"process_id"` etc. on a dedicated HTTP DTO in `localhttp/dto.go` (do not leak store types).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/localhttp/ ./internal/agent/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/procmesh-agent internal/localhttp internal/agent
git commit -m "feat: add procmesh-agent loopback JSON control plane"
```

---

### Task 16: systemd unit and data directories

**Files:**
- Create: `deployments/systemd/procmesh-agent.service`
- Create: `internal/paths/linux.go`
- Create: `internal/paths/darwin.go`

**Interfaces:**
- Consumes: `paths.New`
- Produces:
  - `func paths.DefaultRoot() string` — linux `/var/lib/procmesh`; darwin `~/Library/Application Support/procmesh`
  - unit file:

```ini
[Unit]
Description=ProcMesh Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/procmesh-agent --data-dir /var/lib/procmesh --listen 127.0.0.1:9000
Restart=on-failure
KillMode=process
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 1: Write the failing test**

```go
func TestDefaultRoot_NonEmpty(t *testing.T) {
	if paths.DefaultRoot() == "" {
		t.Fatal("empty")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/paths/ -count=1 -run TestDefaultRoot`
Expected: FAIL until `DefaultRoot` exists.

- [ ] **Step 3: Write implementation**

Use `runtime.GOOS` build tags `linux.go` / `darwin.go`. Other OS: return error from `DefaultRoot` via panic-free `paths.DefaultRoot() string` falling back to `os.TempDir()+"/procmesh"` only in tests; for `unix` generic file `paths/default_other.go` with `//go:build !linux && !darwin` returning `filepath.Join(os.TempDir(), "procmesh")`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/paths/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add deployments/systemd/procmesh-agent.service internal/paths
git commit -m "feat: add systemd unit and platform data-dir defaults"
```

---

### Task 17: P0 acceptance tests (spec cases 3, 5, 10, 11)

**Files:**
- Create: `internal/agent/accept_test.go`

**Interfaces:**
- Consumes: test agent helper from Task 15
- Produces: CI-runnable tests mapping spec §14 cases 3, 5, 10, 11. Case 4 (host reboot) is documented as Linux manual/systemd; automate a boot-id mismatch instead.

- [ ] **Step 1: Write the failing tests**

```go
func TestCase3_AgentCancelDoesNotKillChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base, pid := startSleepAgent(t, ctx)
	cancel()
	time.Sleep(200 * time.Millisecond)
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("child died after agent cancel: %v", err)
	}
	_ = base
}

func TestCase5_ConcurrentCAS(t *testing.T) {
	s := openStoreAt(t, filepath.Join(t.TempDir(), "store.db"))
	ctx := context.Background()
	spec := process.ProcessSpec{ProcessID: "p1", Name: "n", Command: "v1", Instances: 1}
	if _, err := s.PutSpec(ctx, spec, 0, "t", ""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sp := spec
			sp.Command = fmt.Sprintf("v-%d", i)
			_, err := s.PutSpec(ctx, sp, 1, "t", "")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	var conflicts, oks int
	for err := range errs {
		if err == nil {
			oks++
			continue
		}
		if errcode.Is(err, errcode.CONFLICT) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected %v", err)
	}
	if oks != 1 || conflicts != 1 {
		t.Fatalf("oks=%d conflicts=%d", oks, conflicts)
	}
}

func TestCase10_DiskEmergencyNullsNewLogs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := openStoreAt(t, filepath.Join(root, "store.db"))
	layout := paths.New(root)
	_ = layout.Ensure()
	lm := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return 96, nil }, Now: time.Now}
	if _, err := lm.Protect(ctx); err != nil {
		t.Fatal(err)
	}
	if lm.WritesAllowed() {
		t.Fatal("writes should be blocked")
	}
	m := process.NewManager(process.Deps{Store: st, Layout: layout, ShimBin: testShimBin, Now: time.Now, Logs: lm})
	spec := process.ProcessSpec{ProcessID: "p1", Name: "echo", Command: "/bin/echo", Args: []string{"hi"}, Instances: 1}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	stdout, _ := logmgr.InstancePaths(layout, "p1", process.MakeInstanceID("p1", 0))
	if b, _ := os.ReadFile(stdout); len(b) != 0 {
		t.Fatalf("expected no new log bytes, got %q", b)
	}
}

func TestCase11_CorruptDBDoesNotKillProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	base, pid := startSleepAgent(t, ctx)
	db := filepath.Join(testAgentRoot(t), "store.db")
	if err := os.WriteFile(db, []byte("not-a-sqlite-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := http.Post(base+"/v1/processes/p1/start", "application/json", strings.NewReader(`{"operation_id":"op-x","operator":"t"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 503 {
		t.Fatalf("want 503 got %d", res.StatusCode)
	}
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("corrupt db killed process: %v", err)
	}
}
```

`startSleepAgent` lives in `internal/agent/accept_test.go`: create temp root, run `agent.Run` on `127.0.0.1:0`, POST create+start `/bin/sleep 60`, GET process list, parse `pid`. `process.Deps.Logs` is `*logmgr.Manager` added in Task 13; if the field name differs, use the name from `manager.go`.

No `t.Skip` on Linux. Case 10 injects `Usage`; it does not fill a real disk.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -count=1 -run 'TestCase'`
Expected: FAIL until wired (or FAIL compile).

- [ ] **Step 3: Implement any missing glue**

If Case 11 write rejection is missing, Manager/HTTP must check `store.IntegrityCheck` at start and set `agent.Degraded=true`, after which mutations return `errcode.DEGRADED` / HTTP 503. Reads of in-memory recovered instances still allowed.

- [ ] **Step 4: Run full P0 suite**

Run:

```bash
go test ./... -count=1
go test ./internal/process/ ./internal/shim/ ./internal/store/ -cover
```

Expected: all PASS; cover ≥ 80% on those three packages.

- [ ] **Step 5: Commit**

```bash
git add internal/agent
git commit -m "test: add P0 acceptance cases for crash, CAS, disk, and corrupt db"
```

---

## Self-review (plan vs spec)

| Spec item | Task |
|-----------|------|
| SQLite spec/runtime/journal/audit/history | 2–5 |
| revision CAS + rollback new rev | 3 |
| observed + desired state machine | 6 |
| restart never/always/on-failure, backoff, FATAL | 7 |
| local deps DAG + priority | 8 |
| shim proto, setsid, unix socket | 9–11 |
| recover no double-start; orphan UNKNOWN; Adopt | 12 |
| file logs, rotation hooks, disk 85/90/95 | 13 |
| health alive/http/tcp/exec | 14 |
| loopback unauth 127.0.0.1 control | 15 |
| systemd KillMode=process | 16 |
| Cases 3, 5, 10, 11 | 17 |
| macOS cgroup/run_as_user degradation | 1, 10, 12 (`LookUser`) |
| No cluster/raft/web | entire plan omits them |

Out of this plan on purpose (later phases): ConnectRPC CLI, gossip, mTLS RPC, Raft/RBAC, Vue, batch, alerts, Case 1/2/6/7/8/9/12.
