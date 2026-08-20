# Cluster Backup Task 7A Fix Round Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the selector, topology consistency, retry fencing, policy binding, durable delete authorization, and production Peer integration gaps found in the Task 7A scoped review.

**Architecture:** Draft generation runs on the Raft Leader and resolves source selection separately from the full admitted target topology. Peer mutations are authorized by exact durable Raft state: retries first create a non-terminal task transition, Put carries frozen policy identity, and Delete requires an exact unexpired delete intent.

**Tech Stack:** Go, ConnectRPC, protobuf, HashiCorp Raft FSM state, existing Agent mTLS server/client, Go tests.

**Spec:** `docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md` sections 10.4-10.5.

## Global Constraints

- Snapshot payload bytes, storage paths, credentials, certificate material, and raw storage errors never enter Raft, public APIs, logs, or reports.
- Only current same-cluster Agent mTLS identities may access Peer handlers.
- Authorization must complete before any `PeerStore` method is called.
- Generated protobuf files change only through `make proto` and `make proto-ts`.
- Every behavior change follows RED, minimal GREEN, focused regression, then commit.

---

### Task 1: Selector-bound Leader draft generation

**Files:**
- Modify: `internal/backup/topology.go`
- Modify: `internal/backup/topology_test.go`
- Modify: `internal/api/disaster_replication.go`
- Modify: `internal/api/disaster_replication_test.go`
- Modify: `internal/control/fsm.go`
- Modify: `internal/control/fsm_test.go`

**Interfaces:**
- Produces: `GenerateRoutesForSources(nodes []AgentTopology, sourceNodeIDs []string, replicaFactor int, constraints TopologyConstraints) (RouteDraftResult, error)`.
- Produces: one shared selector resolver that returns the exact sorted admitted source IDs for `ALL_ADMITTED`, `EXPLICIT_NODES`, and `AGENT_GROUP`.
- Preserves: `GenerateRoutes` as the all-source compatibility wrapper.

- [ ] **Step 1: Write failing generator/API/FSM tests**

Add tests proving that explicit and group selectors generate only selected source routes while all admitted non-source nodes remain target candidates; FSM rejects missing and extra route sources; follower Generate forwards to Leader; changing only `Alive` leaves `DraftRevision` unchanged.

```go
func TestGenerateRoutesForSources_ExplicitSourceUsesAllAdmittedTargets(t *testing.T) {
    nodes := []backup.AgentTopology{{NodeID: "a", Admitted: true}, {NodeID: "b", Admitted: true}, {NodeID: "c", Admitted: true}}
    got, err := backup.GenerateRoutesForSources(nodes, []string{"a"}, 2, backup.TopologyConstraints{})
    require.NoError(t, err)
    require.Equal(t, []backup.RouteDraft{{SourceNodeID: "a", TargetNodeIDs: []string{"b", "c"}, Warnings: []string{}}}, got.Routes)
}
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/backup ./internal/api ./internal/control -run 'TestGenerateRoutesForSources|TestDisasterReplicationAPI_GeneratePolicyDraft.*Selector|TestDisasterReplicationAPI_GeneratePolicyDraftForwardsLeader|TestTopologyDraftRevisionIgnoresLiveness|TestFSM_ReplicationPolicyPutRouteSourcesMatchSelector' -count=1`

Expected: FAIL because source-aware generation, Leader forwarding, stable liveness revision, and exact FSM source validation are absent.

- [ ] **Step 3: Implement selector-bound generation and validation**

Implement source-aware route generation against the full eligible target set, forward Generate on followers using the existing internal DisasterReplication client, exclude `Alive` from `topologyDraftRevision`, and validate exact route source equality in the FSM using sorted literal sets.

- [ ] **Step 4: Run GREEN**

Run the Step 2 command, then `go test ./internal/backup ./internal/api ./internal/control -run 'Test.*Topology|Test.*Draft|TestFSM_ReplicationPolicy' -count=1`.

- [ ] **Step 5: Commit**

```bash
git add internal/backup/topology.go internal/backup/topology_test.go internal/api/disaster_replication.go internal/api/disaster_replication_test.go internal/control/fsm.go internal/control/fsm_test.go
git commit -m "fix(replication): bind drafts to selected leader topology"
```

### Task 2: Durable retry transition before dispatch

**Files:**
- Modify: `internal/backup/replication_coordinator.go`
- Modify: `internal/backup/replication_coordinator_test.go`
- Modify: `internal/agent/replication_scheduler.go`
- Modify: `internal/agent/replication_scheduler_test.go`
- Modify: `internal/control/fsm.go`
- Modify: `internal/control/fsm_test.go`

**Interfaces:**
- Produces: `BeginReplicationTask(context.Context, ReplicationTaskUpdate) error` on `ReplicationControlPlane`.
- Consumes: the existing immutable run/task snapshot and current Leader term/lease.
- Guarantees: dispatcher sees a task only after Raft records it as `RUNNING`; stale term, expired lease, changed identity, and succeeded task reject before dispatch.

- [ ] **Step 1: Write failing retry transition tests**

Add a real FSM-backed coordinator test starting with an `UNAVAILABLE` task in a live run. Assert that dispatch observes `RUNNING`, succeeds once, and immutable snapshot/checksum remain unchanged. Add stale term/lease and already-succeeded rejection cases.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/backup ./internal/agent ./internal/control -run 'TestReplicationCoordinator_BeginsRetryBeforeDispatch|TestFSM_BeginReplicationTask' -count=1`

Expected: FAIL because dispatch currently occurs directly from failure state.

- [ ] **Step 3: Implement the minimal fenced begin transition**

Add a control-plane begin operation that validates the current run term/lease and exact task identity, changes only retryable tasks to `RUNNING`, clears prior safe error metadata, and rejects terminal success. Call it immediately before each dispatcher invocation.

- [ ] **Step 4: Run GREEN**

Run the Step 2 command, then `go test ./internal/backup ./internal/agent ./internal/control -run 'Test.*Replication.*Retry|Test.*ReplicationCoordinator|TestRunnableReplicationRuns' -count=1`.

- [ ] **Step 5: Commit**

```bash
git add internal/backup/replication_coordinator.go internal/backup/replication_coordinator_test.go internal/agent/replication_scheduler.go internal/agent/replication_scheduler_test.go internal/control/fsm.go internal/control/fsm_test.go
git commit -m "fix(replication): fence route retries before dispatch"
```

### Task 3: Bind PutSnapshot to frozen policy identity

**Files:**
- Modify: `proto/procmesh/v1/api.proto`
- Generate: `proto/procmesh/v1/api.pb.go`
- Generate: `proto/procmesh/v1/procmeshv1connect/api.connect.go`
- Generate: `web/src/gen/procmesh/v1/api_pb.ts`
- Modify: `internal/backup/engine.go`
- Modify: `internal/backup/replication_coordinator.go`
- Modify: `internal/agent/run.go`
- Modify: `internal/agent/replication_scheduler.go`
- Modify: `internal/api/peer_replication.go`
- Modify: `internal/backup/replication_coordinator_test.go`
- Modify: `internal/agent/replication_scheduler_test.go`
- Modify: `internal/api/peer_replication_test.go`

**Interfaces:**
- Extends: `PutSnapshotRequest` with `string policy_id = 7` and `int64 policy_revision = 8`.
- Extends: `ReplicationPushRequest` and `PeerOperation` with `PolicyID string` and `PolicyRevision int64`.
- Guarantees: target authorizer compares both fields to the frozen Raft run before store access.

- [ ] **Step 1: Write failing source propagation and target authorization tests**

Add tests that a mismatched policy ID or revision returns conflict/denied and leaves the target store empty, while exact frozen identity succeeds.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/backup ./internal/api ./internal/agent -run 'Test.*PutSnapshot.*Policy|Test.*ReplicationPush.*Policy' -count=1`

Expected: compile/test failure because policy fields are absent.

- [ ] **Step 3: Extend proto and production propagation**

Modify only `api.proto`, run `make proto` and `make proto-ts`, propagate the frozen policy fields from coordinator to source Engine to Put, and compare them with `ReplicationRuns[runID]` in the target authorizer.

- [ ] **Step 4: Run GREEN**

Run the Step 2 command, then `go test ./internal/backup ./internal/api ./internal/rpc ./internal/agent -run 'TestPeerReplication|Test.*Replication' -count=1`.

- [ ] **Step 5: Commit**

```bash
git add proto/procmesh/v1/api.proto proto/procmesh/v1/api.pb.go proto/procmesh/v1/procmeshv1connect/api.connect.go web/src/gen/procmesh/v1/api_pb.ts internal/backup internal/agent internal/api
git commit -m "fix(replication): bind peer puts to policy revision"
```

### Task 4: Durable exact delete authorization

**Files:**
- Modify: `internal/control/command.go`
- Modify: `internal/control/fsm.go`
- Modify: `internal/control/fsm_test.go`
- Modify: `proto/procmesh/v1/api.proto` and generated outputs through Make targets.
- Modify: `internal/api/peer_replication.go`
- Modify: `internal/api/peer_replication_test.go`
- Modify: `internal/agent/replication_scheduler.go`
- Modify: `internal/agent/replication_scheduler_test.go`

**Interfaces:**
- Produces: `control.ReplicationDeleteIntent` and `State.ReplicationDeleteIntents map[string]ReplicationDeleteIntent`.
- Produces: `CmdReplicationDeleteIntentPut` and `ReplicationDeleteIntentPutBody` with operation ID and exact metadata-only identity.
- Extends: `DeleteSnapshotRequest` with `intent_id`, `policy_id`, and `policy_revision`.
- Guarantees: only a current-term, unexpired `PENDING` intent matching mTLS source, local target, and snapshot authorizes deletion.

- [ ] **Step 1: Write failing FSM and real handler tests**

Cover exact intent success, missing/expired/stale-term/mismatched identity rejection, idempotent missing-file deletion, and rejection before any store access.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/control ./internal/api ./internal/agent -run 'TestFSM_ReplicationDeleteIntent|TestPeerReplicationAPI_DeleteSnapshot.*Intent|TestAuthorizePeerOperationDeleteIntent' -count=1`

Expected: FAIL because durable intents and request fields do not exist and production is deny-all.

- [ ] **Step 3: Implement metadata-only delete intent state and authorizer**

Add backward-compatible initialized state, strict command validation, exact target-side matching, and stable default denial. Do not add paths, payloads, or storage errors to the intent.

- [ ] **Step 4: Run GREEN and regenerate proto outputs**

Run `make proto`, `make proto-ts`, the Step 2 command, then `go test ./internal/control ./internal/api ./internal/rpc ./internal/agent -run 'TestPeerReplication|Test.*DeleteIntent' -count=1`.

- [ ] **Step 5: Commit**

```bash
git add internal/control internal/api internal/agent proto/procmesh/v1 web/src/gen/procmesh/v1/api_pb.ts
git commit -m "fix(replication): authorize exact peer retention deletes"
```

### Task 5: Production mTLS authorization integration and regressions

**Files:**
- Modify: `internal/agent/rpc_test.go`
- Modify: `internal/agent/replication_scheduler_test.go`
- Modify: `.superpowers/sdd/2026-08-20-cluster-backup-completion/task-7a-report.md`

**Interfaces:**
- Consumes: real Agent RPC server, real control state, generated Peer client, and temporary PeerStore.
- Produces: integration evidence for authorized Put, selector isolation, stale fencing, policy mismatch, exact Delete intent, and no-write denials.

- [ ] **Step 1: Add a real control-state mTLS integration test**

Start a real TLS RPC runtime with an admitted source and target, a live frozen run/task, and exact policy revision. Verify authorized Put succeeds; then mutate one immutable field at a time and assert PermissionDenied/FailedPrecondition plus unchanged store state.

- [ ] **Step 2: Run the focused integration test**

Run: `go test ./internal/agent -run 'TestRPCRuntime_PeerAuthorization' -count=1`

Expected: PASS after Tasks 1-4; any failure is a cross-component contract defect to fix before regressions.

- [ ] **Step 3: Run required regressions and static checks**

```bash
go test ./internal/backup ./internal/control ./internal/api ./internal/rpc ./internal/agent -count=1
go vet ./internal/backup ./internal/control ./internal/api ./internal/rpc ./internal/agent
git diff --check
```

- [ ] **Step 4: Record exact evidence and residual risks**

Append RED/GREEN commands, outputs, generated-file commands, modified files, security decisions, and any remaining retention-planner limitation to `task-7a-report.md`.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/rpc_test.go internal/agent/replication_scheduler_test.go
git commit -m "test(replication): cover production peer authorization"
```
