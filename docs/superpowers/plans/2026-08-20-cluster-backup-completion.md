# Cluster Backup and Disaster Replication Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close every code gap between the 2026-08-19 design and the current partial implementation, then prove the feature with production wiring, Web workflows, and multi-Agent acceptance tests.

**Architecture:** Keep Raft authoritative only for policies, fire ledgers, runs, and task metadata. The Leader coordinates stable task IDs while each Agent creates or transfers payloads directly through local sinks and the existing mTLS RPC plane; the public ConnectRPC services expose control metadata to the Vue application.

**Tech Stack:** Go, HashiCorp Raft, ConnectRPC, SQLite, FS/S3 sinks, mTLS, Vue 3, Vue Query, Vue Router, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md`

## Global Constraints

- Preserve the legacy `BackupService`, local `backup.schedule`, and API compatibility paths.
- Never store snapshot payloads, S3 secrets, or local index paths in Raft, Gossip, audit data, logs, or public API responses.
- Restore remains Owner-only and must call `ApplySpec` with `expected_revision`.
- All control mutations require a non-empty `operation_id`; all Agent tasks use stable run/task IDs and checksums.
- Generated protobuf files are changed only through `make proto` and `make proto-ts`.
- Every behavior change follows RED, GREEN, focused regression, then phase regression.
- Existing dirty files in the original checkout are not copied, reverted, or overwritten.

## Baseline

- Worktree: `.worktrees/cluster-backup-completion`
- Branch: `codex/cluster-backup-completion`
- Go baseline: backup/API/RPC/auth passed; `internal/control/TestNode_IsVoter` failed once and must be diagnosed for determinism.
- Web baseline: 285 tests passed; four `ProcessesPage.test.ts` tests fail because the fixture does not inject Vue Router.
- i18n baseline: complete and consistent.

---

### Task 1: Internal mTLS request identity and Peer namespace safety

**Files:**
- Modify: `internal/rpc/server.go`
- Modify: `internal/rpc/server_test.go`
- Modify: `internal/backup/peer.go`
- Modify: `internal/backup/peer_transfer_test.go`

**Interfaces:**
- Produces: every internal HTTP request receives `*tls.ConnectionState` through `Request.Context()` and `rpc.TLSStateFromContext`.
- Produces: `PeerStore` accepts only single-segment source, cluster, and snapshot identifiers before joining paths.

- [x] **Step 1: Write failing TLS propagation and traversal tests**

```go
func TestServerInjectsTLSStateIntoRequestContext(t *testing.T)
func TestPeerStoreRejectsTraversalIdentifiers(t *testing.T)
```

The TLS handler calls `TLSStateFromContext`; traversal cases include `../other`, absolute paths, separators, `.` and `..` for every path component.

- [x] **Step 2: Run RED**

Run: `go test ./internal/rpc ./internal/backup -run 'TestServerInjectsTLSState|TestPeerStoreRejectsTraversal' -count=1`

Expected: TLS state is absent in production server requests and unsafe identifiers are accepted or reach path construction.

- [x] **Step 3: Implement the request wrapper and shared identifier validation**

Wrap the internal handler before assigning `http.Server.Handler`:

```go
ctx := WithTLSState(r.Context(), *r.TLS)
next.ServeHTTP(w, r.WithContext(ctx))
```

Reject empty, `.`/`..`, absolute, non-base, slash, backslash, and NUL-containing identifiers with `INVALID_ARGUMENT` before `filepath.Join`.

- [x] **Step 4: Run focused and package tests**

Run: `go test ./internal/rpc ./internal/backup -count=1`

- [x] **Step 5: Commit**

```bash
git add internal/rpc internal/backup
git commit -m "fix(replication): secure internal peer request paths"
```

### Task 2: Named S3 destination profiles and capability validation

**Files:**
- Modify: `internal/agentcfg/load.go`
- Modify: `internal/agentcfg/load_test.go`
- Modify: `internal/agent/backup_scheduler.go`
- Modify: `internal/api/cluster_backup.go`
- Modify: `internal/api/cluster_backup_test.go`

**Interfaces:**
- Produces: `AgentConfig.Backup.S3Profiles map[string]S3Profile` with endpoint, bucket, prefix, region, access key, and secret key.
- Produces: local resolver returns sink configuration without exposing secrets.
- Produces: policy validation/health reports `CONFIG_MISSING` per frozen target.

- [x] **Step 1: Write failing config, redaction, and health tests**
- [x] **Step 2: Run RED:** `go test ./internal/agentcfg ./internal/agent ./internal/api -run 'Test.*S3Profile|Test.*DestinationHealth' -count=1`
- [x] **Step 3: Parse named profiles, inject the resolver into task execution, and return only profile name, endpoint host, and status from APIs.**
- [x] **Step 4: Run:** `go test ./internal/agentcfg ./internal/agent ./internal/api ./internal/backup -count=1`
- [x] **Step 5: Commit:** `git commit -m "feat(backup): resolve named s3 destination profiles"`

### Task 3: Remote backup task RPC and manual-run dispatch

**Files:**
- Modify: `proto/procmesh/v1/api.proto` and generated Go/TS files
- Modify: `internal/api/cluster_backup_agent.go`
- Modify: `internal/api/cluster_backup.go`
- Modify: `internal/agent/rpc.go`
- Modify: `internal/agent/backup_scheduler.go`
- Modify: `internal/rpc/client.go`

**Interfaces:**
- Produces: internal `ExecuteClusterBackupTask` accepts stable run/task/node/policy IDs, term, lease, sink, and profile.
- Produces: `ClusterBackupAPI.StartRun` freezes targets, creates tasks, and dispatches both local and remote targets.
- Produces: duplicate dispatch returns the existing successful snapshot metadata.

- [x] **Step 1: Write failing tests for remote dispatch, manual dispatch, unavailable targets, and idempotent replay.**
- [x] **Step 2: Run RED:** `go test ./internal/api ./internal/agent ./internal/rpc -run 'Test.*ClusterBackup.*Dispatch|TestStartRun.*Task' -count=1`
- [x] **Step 3: Add/regenerate the internal RPC, use the mTLS Agent client for remote nodes, and invoke the same dispatcher from scheduled and manual runs.**
- [x] **Step 4: Run:** `go test ./internal/api ./internal/agent ./internal/rpc ./internal/backup -count=1`
- [x] **Step 5: Commit:** `git commit -m "feat(backup): dispatch cluster tasks to target agents"`

### Task 4: Coordinator recovery, aggregation, timeout, retry, and fail-fast

**Files:**
- Modify: `internal/backup/coordinator.go`
- Modify: `internal/backup/coordinator_test.go`
- Modify: `internal/control/command.go`
- Modify: `internal/control/fsm.go`
- Modify: `internal/control/fsm_test.go`
- Modify: `internal/api/cluster_backup.go`

**Interfaces:**
- Produces: stable task state transitions fenced by Leader term and lease.
- Produces: run terminal status `SUCCEEDED`, `PARTIAL`, `FAILED`, or `CANCELED` from frozen task results.
- Produces: retry selects only failed/timeout/unavailable/config-missing tasks and preserves successful task IDs/results.

- [ ] **Step 1: Write failing tests for aggregation matrix, timeout, fail-fast, retry selection, stale-term rejection, and new-Leader resume.**
- [ ] **Step 2: Run RED:** `go test ./internal/backup ./internal/control ./internal/api -run 'Test.*Aggregate|Test.*Timeout|Test.*FailFast|Test.*RetryFailed|Test.*Resume' -count=1`
- [ ] **Step 3: Add durable run finalization/CAS commands and make the coordinator reconcile expired running runs before evaluating new fires.**
- [ ] **Step 4: Run:** `go test ./internal/backup ./internal/control ./internal/api -count=1`
- [ ] **Step 5: Commit:** `git commit -m "feat(backup): finalize and recover cluster backup runs"`

### Task 5: Complete retention and compatibility behavior

**Files:**
- Modify: `internal/backup/retention.go`
- Modify/create: `internal/backup/retention_test.go`
- Modify: `internal/backup/fs.go`, `internal/backup/s3.go`, `internal/backup/peer.go`
- Modify: `internal/backup/schedule.go`
- Modify: `internal/backup/schedule_compat_test.go`

**Interfaces:**
- Produces: sink-specific deletion under generated namespace only.
- Produces: `keep_last`, timezone-aware `keep_days`, and `max_bytes` while preserving active restore/copy and last usable replica.
- Produces: `RETENTION_FAILED` metadata with retryability.

- [ ] **Step 1: Write failing boundary tests for all retention dimensions and legacy path/schedule regression.**
- [ ] **Step 2: Run RED:** `go test ./internal/backup -run 'TestRetention|TestScheduleCompatibility|TestLegacy' -count=1`
- [ ] **Step 3: Implement a planner over metadata and separate FS/S3/Peer deleters; wire it after run/route terminal transitions.**
- [ ] **Step 4: Run:** `go test ./internal/backup ./internal/api ./internal/agent -count=1`
- [ ] **Step 5: Commit:** `git commit -m "feat(backup): enforce backup and replica retention"`

### Task 6: Public disaster-replication API and correct run creation

**Files:**
- Modify: `internal/api/server.go`
- Modify: `internal/api/server_test.go`
- Modify: `internal/api/disaster_replication.go`
- Modify: `internal/api/disaster_replication_test.go`

**Interfaces:**
- Produces: public `DisasterReplicationService` mounted with auth and Leader forwarding.
- Produces: `StartRun` uses the current non-zero Leader term and creates stable route tasks before returning.

- [ ] **Step 1: Write failing mount/forward and non-zero-term route-task tests.**
- [ ] **Step 2: Run RED:** `go test ./internal/api -run 'TestServer.*DisasterReplication|TestReplicationAPI_StartRun' -count=1`
- [ ] **Step 3: Mount the service using the ClusterBackup forwarding pattern and create the run/tasks in fenced control commands.**
- [ ] **Step 4: Run:** `go test ./internal/api ./internal/control -count=1`
- [ ] **Step 5: Commit:** `git commit -m "feat(replication): expose and start disaster replication runs"`

### Task 7: Runnable replication coordinator and recoverable snapshots

**Files:**
- Create: `internal/backup/replication_coordinator.go`
- Create: `internal/backup/replication_coordinator_test.go`
- Modify: `internal/api/disaster_replication.go`
- Modify: `internal/api/peer_replication.go`
- Modify: `internal/agent/run.go`, `internal/agent/rpc.go`

**Interfaces:**
- Produces: Leader-only schedule/manual/after-primary route execution with per-route retry and concurrency limits.
- Produces: source payload is loaded by snapshot ID/checksum from staging or primary sink, never regenerated from current specs.
- Produces: `ListRecoverableSnapshots` merges local index/Peer metadata and retains source Owner identity.

- [ ] **Step 1: Write failing tests for each trigger, route-only retry, checksum conflict, source reload, terminal aggregation, and recoverable listing.**
- [ ] **Step 2: Run RED:** `go test ./internal/backup ./internal/api ./internal/agent -run 'TestReplicationCoordinator|Test.*Recoverable|Test.*AfterPrimary' -count=1`
- [ ] **Step 3: Implement and wire the replication loop using direct mTLS Peer calls and small Raft status updates.**
- [ ] **Step 4: Run:** `go test ./internal/backup ./internal/api ./internal/rpc ./internal/agent -count=1`
- [ ] **Step 5: Commit:** `git commit -m "feat(replication): execute and recover peer replicas"`

### Task 8: RBAC, audit, metrics, and secret redaction

**Files:**
- Modify: `internal/api/role.go`, `internal/api/rbac.go`
- Modify: `internal/api/*backup*_test.go`, `internal/api/disaster_replication_test.go`
- Modify: `internal/api/metrics.go`, `internal/api/metrics_test.go`
- Modify: audit event builders/tests

**Interfaces:**
- Produces: custom roles may grant `replication.read/manage`.
- Produces: every documented mutation emits an audit record containing IDs but no payload or secret.
- Produces: bounded-label policy/run/task/route/bytes/duration/retention metrics.

- [ ] **Step 1: Write failing permission, audit redaction, and metric family tests.**
- [ ] **Step 2: Run RED:** `go test ./internal/auth ./internal/api -run 'Test.*Replication.*Role|Test.*Backup.*Audit|Test.*Replication.*Audit|Test.*Backup.*Metric' -count=1`
- [ ] **Step 3: Extend the role allowlist, centralize mutation audit events, and register/update the documented metric families without URL/node cardinality leaks.**
- [ ] **Step 4: Run:** `go test ./internal/auth ./internal/api ./internal/store -count=1`
- [ ] **Step 5: Commit:** `git commit -m "feat(backup): audit and observe backup replication controls"`

### Task 9: Web clients, route, navigation, and baseline fixture repair

**Files:**
- Modify: `web/src/lib/rpc.ts`, `web/src/router.ts`
- Modify: `web/src/components/AppShell.vue`, `web/src/components/AppShell.test.ts`
- Modify: `web/src/pages/ProcessesPage.test.ts`
- Create: `web/src/pages/DisasterReplicaPage.vue`, `web/src/pages/DisasterReplicaPage.test.ts`

- [ ] **Step 1: Write failing client/navigation/permission tests; retain the existing failing ProcessesPage reproduction.**
- [ ] **Step 2: Run RED:** `cd web && npm test -- --run src/components/AppShell.test.ts src/pages/DisasterReplicaPage.test.ts src/pages/ProcessesPage.test.ts`
- [ ] **Step 3: Add generated service clients, permission-gated navigation and route, then inject a real memory router in the ProcessesPage fixture.**
- [ ] **Step 4: Run the three focused test files.**
- [ ] **Step 5: Commit:** `git commit -m "feat(web): add backup replication navigation and clients"`

### Task 10: Cluster backup policy and run Web workflow

**Files:**
- Modify: `web/src/pages/BackupPage.vue`, `web/src/pages/BackupPage.test.ts`

- [ ] **Step 1: Write failing tests for policy CRUD, FS/S3-only sink selection, destination health, manual runs, per-Agent detail, partial state, and failed-only retry.**
- [ ] **Step 2: Run RED:** `cd web && npm test -- --run src/pages/BackupPage.test.ts`
- [ ] **Step 3: Implement dense policy/run tables, dialogs and polling using existing Query/Freshness patterns; preserve legacy snapshot restore.**
- [ ] **Step 4: Run focused tests and `npm run build`.**
- [ ] **Step 5: Commit:** `git commit -m "feat(web): manage cluster backup policies and runs"`

### Task 11: Disaster replica Web workflow, i18n, and embedded assets

**Files:**
- Modify: `web/src/pages/DisasterReplicaPage.vue`, `web/src/pages/DisasterReplicaPage.test.ts`
- Modify: `web/public/locales/en/common.json`, `web/public/locales/zh/common.json`
- Generate: `web/src/types/i18n.d.ts`, `internal/web/dist/**`

- [ ] **Step 1: Write failing tests for topology draft/preview/apply, warnings/load, runs, route retry, verify, recoverable snapshots, freshness and responsive dialog semantics.**
- [ ] **Step 2: Run RED:** `cd web && npm test -- --run src/pages/DisasterReplicaPage.test.ts && npm run i18n:check`
- [ ] **Step 3: Implement the page, synchronized locale keys, and remove Peer from the primary backup sink UI.**
- [ ] **Step 4: Run:** `cd web && npm run i18n:check && npm test -- --run && npm run build`
- [ ] **Step 5: Commit:** `git commit -m "feat(web): complete disaster replica workflow"`

### Task 12: Three-Agent acceptance, failover, S3/Peer/restore safety, and docs

**Files:**
- Create: `internal/agent/cluster_backup_accept_test.go`
- Modify/create: focused Agent/API/backup acceptance tests
- Create: `web/e2e/cluster-backup.spec.ts`, `web/e2e/disaster-replica.spec.ts`
- Modify: architecture/user documentation cross references

- [ ] **Step 1: Write failing three-Agent tests covering FS namespace, partial/unavailable, S3 keys/redaction, Leader loss at three boundaries, Peer idempotency/conflict, and Owner CAS restore.**
- [ ] **Step 2: Run RED:** `go test ./internal/agent ./internal/api ./internal/backup -run 'TestClusterBackup_|TestDisasterReplication_|TestRestore_' -count=1 -timeout 240s`
- [ ] **Step 3: Fix integration wiring only, add deterministic clocks/leases, update documentation, and add browser E2E for both workflows.**
- [ ] **Step 4: Run full acceptance:**

```bash
go test ./... -count=1
cd web
npm run i18n:check
npm test -- --run
npm run build
npm run test:e2e -- cluster-backup.spec.ts disaster-replica.spec.ts
cd ..
git diff --check
```

- [ ] **Step 5: Commit:** `git commit -m "test(backup): complete cluster backup disaster recovery acceptance"`
