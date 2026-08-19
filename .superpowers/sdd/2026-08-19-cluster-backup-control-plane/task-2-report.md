# Task 2 Report: Fire Ledger and Run/Task Metadata

## Implementation Summary

- Added bounded Raft metadata types `FireRecord`, `ClusterBackupRun`, and `ClusterBackupTask`.
- Added backup fire ledger, backup run/task maps, replication run/task maps, and run-term fencing maps to `control.State`.
- Added `ClaimFire`, `CreateRun`, `UpdateTask`, `FinishRun`, and `PruneRunMetadata` with operation ID validation, fire/run idempotency, lease takeover, stale-term rejection, terminal task preservation, and pruning.
- Added Raft command constants/body types and FSM dispatch.
- Added focused tests for fire idempotency, distinct runs, lease takeover, stale-term rejection, terminal task idempotency, replication metadata, pruning, and snapshot safety. Snapshot bytes are asserted not to contain `payload`, `secret_key`, `access_key`, or `backup_index`.

## RED Verification

Command: `go test ./internal/control -run 'TestFSM_FireLedger|TestFSM_BackupRun' -count=1`

Relevant failure output before implementation:

`internal/control/fsm_test.go:322:27: s.ClaimFire undefined (type *control.State has no field or method ClaimFire)`

`internal/control/fsm_test.go:322:45: undefined: control.FireClaimBody`

`internal/control/fsm_test.go:347:17: undefined: control.ClusterBackupRun`

`FAIL github.com/qleelulu/procmesh/internal/control [build failed]`

## GREEN and Full Verification

`go test ./internal/control -run 'TestFSM_FireLedger|TestFSM_BackupRun' -count=1` -> `ok github.com/qleelulu/procmesh/internal/control 0.290s`

`go test ./internal/control -count=1` -> `ok github.com/qleelulu/procmesh/internal/control 12.503s`

`go vet ./internal/control` and `git diff --check` completed successfully with no output/errors. The focused snapshot test inspected persisted FSM snapshot bytes for all four forbidden strings.

## Changed Files

- `internal/control/command.go`
- `internal/control/fsm.go`
- `internal/control/fsm_test.go`

## Commit

Implementation commit: `956c0fc4cf3d5681a81a9eff414e7443abfd1654` — `feat(backup): add fire ledger and cluster run metadata`.
Fencing follow-up: `c85a648fe7e099fec7c65787d734965180bfce40` — includes the stale first-task update guard and its regression test.

## Self-Review Findings

- Metadata structs contain only IDs, status, timestamps, counts, checksum, and bounded error summary/code fields.
- No snapshot payload, credentials, local backup index, or sensitive path fields were added.
- All metadata mutation bodies require a non-empty `operation_id`.
- Newer leader terms fence stale run/task updates; terminal task results are immutable under repeated updates.
- Replication run/task maps use the same bounded metadata behavior.

## Concerns

- No known concerns within Task 2 scope. The full package test had one unrelated transient Raft voter failure and passed on the subsequent rerun.

## Review Round 1

Addressed review findings:

- Run terminal statuses now recognize `SUCCEEDED`, `PARTIAL`, `FAILED`, and `CANCELED`; task terminal statuses recognize `SUCCEEDED`, `FAILED`, `TIMEOUT`, `UNAVAILABLE`, `CONFIG_MISSING`, and `SKIPPED`. Legacy `SUCCESS` remains accepted as a compatibility alias.
- Added bounded validation for metadata IDs, fire keys, error code/summary, snapshot ID, checksum, task status, and target node count/ID length before state mutation.
- Fire pruning now retains live/non-expired claims and removes only expired claims or old terminal claims.
- `CreateRun` now requires an existing policy, matching revision, bounded/frozen targets, and exact explicit policy targets for backup and replication policies.

Review RED command:

```bash
go test ./internal/control -run 'TestFSM_MetadataUsesSpec|TestFSM_MetadataBounds|TestFSM_PruneRunMetadataRetainsLive|TestFSM_CreateRunValidates' -count=1
```

Relevant RED output:

```text
--- FAIL: TestFSM_MetadataUsesSpecTerminalStatuses
    fsm_test.go:442: terminal task changed: ... Status:FAILED ... SHA256:changed
--- FAIL: TestFSM_MetadataBoundsRejectOversizedInputs
    fsm_test.go:469: case 0: <nil>
--- FAIL: TestFSM_PruneRunMetadataRetainsLiveFireLease
    fsm_test.go:495: live lease pruned
--- FAIL: TestFSM_CreateRunValidatesAndFreezesPolicy
    fsm_test.go:517: revision: <nil>
FAIL
```

Review GREEN/full verification:

```bash
go test ./internal/control -run 'TestFSM_MetadataUsesSpec|TestFSM_MetadataBounds|TestFSM_PruneRunMetadataRetainsLive|TestFSM_CreateRunValidates|TestFSM_FireLedger|TestFSM_BackupRun' -count=1
```

Output: `ok github.com/qleelulu/procmesh/internal/control 0.334s`

```bash
go test ./internal/control -count=1
```

Output: `ok github.com/qleelulu/procmesh/internal/control 12.451s`

`go vet ./internal/control` and `git diff --check` also completed successfully.
