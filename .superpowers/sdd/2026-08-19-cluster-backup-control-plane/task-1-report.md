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
