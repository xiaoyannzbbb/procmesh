# Task 1 Report: Raft Policy Records and Validation

## Implementation Summary

- Added Raft commands and JSON bodies for backup and replication policy put/delete operations.
- Added `control.State.BackupPolicies` and `ReplicationPolicies`, including nil-map initialization through `EnsureForTest`/`ensure`.
- Added control-shaped `BackupPolicy` and `ReplicationPolicy` records with revision tracking and explicit validation.
- Added validation for policy names, five-field cron, IANA timezone, selectors, sinks, S3 destination profile, retention/limit ranges, replica factor, self-replication, duplicate targets, and missing deletes.
- Added dependency-light `internal/schedule` cron/timezone validation and reused it from `internal/backup` and `internal/control`.
- Added `internal/backup.PolicyRecord`, `Policy`, and `PolicyFromRecord` conversion without importing `internal/control`; target ID slices are copied.
- No snapshot payload, full process spec, S3 credentials, or local backup index was added to Raft policy state.

## RED Evidence

Exact command:

```text
go test ./internal/control -run 'TestFSM_BackupPolicy|TestFSM_ReplicationPolicy' -count=1
```

Exact failure excerpt:

```text
internal/control/fsm_test.go:873:16: undefined: control.BackupPolicyPutBody
internal/control/fsm_test.go:878:42: undefined: control.CmdBackupPolicyPut
internal/control/fsm_test.go:881:14: s.BackupPolicies undefined (type *control.State has no field or method BackupPolicies)
...
too many errors
FAIL github.com/qleelulu/procmesh/internal/control [build failed]
```

This failed as expected because the new commands, record types, validators, and state maps did not yet exist.

## GREEN / Final Verification

```text
go test ./internal/control -run 'TestFSM_BackupPolicy|TestFSM_ReplicationPolicy' -count=1
ok   github.com/qleelulu/procmesh/internal/control  0.259s

go test ./internal/schedule ./internal/backup -count=1
ok   github.com/qleelulu/procmesh/internal/schedule 0.186s
ok   github.com/qleelulu/procmesh/internal/backup   0.609s

go vet ./internal/control ./internal/schedule ./internal/backup
exit 0

git diff --check
exit 0

go test ./internal/control -count=1
ok   github.com/qleelulu/procmesh/internal/control 11.624s
```

An earlier combined verification invocation hit the existing timing-sensitive `TestNode_IsVoter` failure; the required full control command was rerun standalone and passed.

## Files Changed

- `internal/control/command.go`
- `internal/control/fsm.go`
- `internal/control/fsm_test.go`
- `internal/schedule/cron.go`
- `internal/schedule/cron_test.go`
- `internal/backup/schedule.go`
- `internal/backup/cluster_policy.go`
- `internal/backup/cluster_policy_test.go`

## Commits

- `9c5628b feat(backup): persist cluster backup policies in raft`

## Self-Review

- Confirmed `internal/backup` and `internal/schedule` do not import `internal/control`.
- Confirmed policy records contain only control metadata and no payload, process specs, credentials, or backup-index fields.
- Confirmed explicit-node backup policies require admitted members; candidate-count limits remain outside FSM policy persistence.
- Confirmed policy and route slices/maps are copied before storage/conversion to avoid caller mutation aliasing.
- Confirmed focused tests cover valid FS/S3 policies, invalid fields, unsafe replication routes, missing deletes, nil-map initialization, shared cron/timezone validation, and explicit backup conversion.

## Concerns

- The existing `TestNode_IsVoter` can be timing-sensitive when several control package tests run in one process; the standalone full control run passed.
- Generated proto files and later coordinator/API integration remain intentionally out of scope for this task.

## Fix Round 1

### Covering Tests

- `TestFSM_BackupPolicyPutRejectsInvalidFields` covers invalid unavailable policy and an unknown explicit node.
- `TestFSM_BackupPolicyPutDefaultsAndValidatesUnavailablePolicy` confirms the empty unavailable policy normalizes to `RECORD_AND_CONTINUE`.
- `TestFSM_PolicyPutRejectsInvalidAgentGroupSelectors` covers blank, duplicate, and missing Agent Group IDs for backup targets and replication sources.
- `TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes` covers allowed trigger handling, invalid trigger, scheduled cron/timezone requirements, primary-policy reference requirements, negative replication retention/limit values, invalid explicit sources, and invalid route source/target IDs.

### RED Evidence

Exact command:

```text
go test ./internal/control -run 'TestFSM_BackupPolicy|TestFSM_ReplicationPolicy|TestFSM_PolicyPut' -count=1
```

Exact output before the validation changes:

```text
--- FAIL: TestFSM_BackupPolicyPutRejectsInvalidFields (0.00s)
    --- FAIL: TestFSM_BackupPolicyPutRejectsInvalidFields/invalid_unavailable_policy (0.00s)
        fsm_test.go:923: got <nil>
--- FAIL: TestFSM_BackupPolicyPutDefaultsAndValidatesUnavailablePolicy (0.00s)
    fsm_test.go:940: unavailable policy=""
--- FAIL: TestFSM_PolicyPutRejectsInvalidAgentGroupSelectors (0.00s)
    fsm_test.go:957: backup bp-group-missing: <nil>
--- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes (0.00s)
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/invalid_trigger (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/scheduled_trigger_missing_schedule (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/after_primary_missing_policies (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/after_primary_blank_policy (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/after_primary_unknown_policy (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/after_primary_duplicate_policy (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/negative_retention (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/negative_concurrency (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/negative_bandwidth (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/blank_route_target (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/unknown_explicit_source (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/unknown_route_target (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/revoked_route_target (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/unknown_route_source (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/duplicate_explicit_sources (0.00s)
        fsm_test.go:1199: got <nil>
    --- FAIL: TestFSM_ReplicationPolicyPutRejectsUnsafeRoutes/blank_explicit_source (0.00s)
        fsm_test.go:1199: got <nil>
FAIL
FAIL github.com/qleelulu/procmesh/internal/control 0.260s
FAIL
```

This RED result showed the missing trigger, schedule, member/group reference, replication limit, and unavailable-policy validation. A second RED iteration showed `SCHEDULE` still accepted an empty cron because shared cron validation deliberately permits an empty manual schedule; the trigger-specific nonempty check was then added.

### GREEN Verification

```text
go test ./internal/control -run 'TestFSM_BackupPolicy|TestFSM_ReplicationPolicy|TestFSM_PolicyPut' -count=1
ok   github.com/qleelulu/procmesh/internal/control 0.352s

go test ./internal/control -count=1
ok   github.com/qleelulu/procmesh/internal/control 12.629s

go test ./internal/schedule ./internal/backup -count=1
ok   github.com/qleelulu/procmesh/internal/schedule 0.183s
ok   github.com/qleelulu/procmesh/internal/backup   0.619s

go vet ./internal/control ./internal/schedule ./internal/backup
exit 0

git diff --check
exit 0
```

### Files Changed

- `internal/control/fsm.go`
- `internal/control/fsm_test.go`
- `.superpowers/sdd/2026-08-19-cluster-backup-control-plane/task-1-report.md`

### Self-Review

- `BackupPolicy.UnavailablePolicy` now explicitly defaults only when empty and otherwise accepts only `RECORD_AND_CONTINUE` or `FAIL_FAST`.
- Replication policies accept only `AFTER_PRIMARY_BACKUP`, `SCHEDULE`, or `MANUAL`; scheduled policies require nonempty shared-valid cron/timezone, while primary-triggered policies reference existing, unique, nonblank backup policy IDs.
- Explicit node selectors and every persisted route endpoint require admitted members. Agent Group selectors require existing, unique, nonblank group IDs without resolving group membership at policy write time.
- Source IDs, primary policy IDs, and route IDs reject surrounding whitespace rather than preserving unusable references.

### Concerns

- Replication route candidate-count/factor limits remain intentionally outside FSM persistence because selector-only policies cannot infer the current member set.
