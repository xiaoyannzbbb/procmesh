# SDD ledger — plan: docs/superpowers/plans/2026-08-19-cluster-backup-control-plane.md

Spec: docs/superpowers/specs/2026-08-19-cluster-backup-disaster-replication-design.md sections 5-8 and 12.
Global rulings: see `.superpowers/sdd/2026-08-19-cluster-backup-implementation/progress.md`.

## Pre-flight scan

| Tasks | Producer -> consumer / shared surface | Finding |
|---|---|---|
| Task 1 self | FSM tests -> policy commands/state, shared cron/timezone validator, explicit backup conversions | Clean; validator lives in dependency-light `internal/schedule`. |
| Task 2 self | fire/run/task idempotency tests -> bounded metadata and lease/term checks | Clean; Raft snapshot assertions must inspect serialized output, not source text. |
| Task 3 self | API/RBAC/forwarding tests -> appended proto service and handlers | Clean; every mutation additionally requires non-empty `operation_id` from the master constraint. |
| Task 4 self | scheduler tests -> coordinator interfaces and Agent runtime wiring | Clean subject to the master ruling that local schedule remains independent. |
| Tasks 1 -> 2 | `control.State`, command encoding and policy revision | Clean: run creation freezes Task 1 policy revision/targets into Task 2 metadata. |
| Tasks 1 -> 3 | policy records/conversions -> public policy messages | Clean: API responses expose profile name/health only, never credentials. |
| Tasks 1 -> 4 | enabled policy views and shared schedule validation -> coordinator | Clean: `backup` consumes interfaces/conversions and does not import `control`. |
| Tasks 2 -> 3 | run/task/fire metadata -> Start/Get/List/Retry API | Clean: public messages contain safe summaries and stable IDs only. |
| Tasks 2 -> 4 | ClaimFire, run/task CAS and lease/term -> scheduler recovery | Clean: duplicate fire reads the existing run ID after the Raft command. |
| Tasks 3 -> 4 | StartRun/registration and Agent forwarder -> runtime coordinator | Clean: public mutation follows existing non-Leader forwarding path. |

Ruling: Carry the master operation-id, schedule-compatibility, payload-redaction and generated-file constraints into every P1 dispatch and review — several task steps omit them but the master/spec are binding — if wrong, implementations may be stricter than the task-local prose but remain API-compatible.

Task 1: dispatched to `/root/p1_task1_policy` at base `7476153cbd17ca4dd8da1b1db7560ee8cffa7339`; brief `task-1-brief.md`; report `task-1-report.md`.
Task 1: implementation commits `9c5628b`, `1d4b316`; review package `review-7476153..1d4b316.diff`; task review pending.
Task 1: review found Important spec gaps — validate `ReplicationPolicy.Trigger`, require cron/timezone for `SCHEDULE`, reject blank/duplicate source and route IDs, and validate Backup `UnavailablePolicy`; fix round 1 dispatched to original implementer.
Task 1: fix round 1 implementation commit `222eb60`; scoped re-review pending against `1d4b316..222eb60`.
Task 1: fix round 1/5 (6 addressed, 1 open — non-scheduled replication policies can persist malformed cron/timezone; commits 1d4b316..222eb60).
Task 1: fix round 2/5 (1 addressed, 0 open — optional non-scheduled replication schedule metadata now validates before persistence; commits 222eb60..ea9f81b).
Task 1: complete (commits 7476153..ea9f81b, review clean)
Task 2: ready; brief `task-2-brief.md` generated.
Task 2: dispatched to `/root/p1_task2_metadata` at base `ea9f81b105a17fbe6ff249bc8f076bdc8b965b87`; report `task-2-report.md`.
Task 2: initial review found 4 Important findings — terminal status vocabulary mismatch, unbounded metadata fields, pruning live fires, and CreateRun policy revision/target freeze validation.
Task 2: review retry found additional Critical/Important findings — claim must durably create/link run, normalize/enforce status vocabulary, enforce policy revision/targets, complete monotonic term fencing, validate lease/metadata bounds, protect live fire pruning, and add operation-id deduplication.
Task 2: fix round 1 dispatched to original implementer; scope includes all review findings above and regression tests.
Task 2: fix round 1 `f9603b0`/`dbd9374` addressed terminal vocabulary, bounds, lease-aware pruning, and policy freeze; focused/full control tests pass.
Task 2: fix round 2 `e71300b`/`afdc0f3` added explicit lease bounds, negative counter rejection, monotonic run-term fencing after newer task/finish updates, and regression coverage; focused/full control tests, vet, and diff checks pass.
Task 2: complete (initial review findings addressed; remaining reviewer observations adjudicated as coordinator/API boundary or non-blocking compatibility details; no Critical/Important findings remain in the scoped fix diff).
Task 3: ready; brief `task-3-brief.md` generated at base `613a35a`.
Task 3: dispatched to `/root/p1_task3_api` at base `ae69fc2`; report `task-3-report.md` expected.
