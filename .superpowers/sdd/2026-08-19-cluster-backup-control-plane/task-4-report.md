# Task 4 Report

Implemented leader-only cluster backup scheduling and coordinator wiring.

Files changed:

- `internal/backup/coordinator.go` and `coordinator_test.go`: scheduler interfaces, timezone-aware fire evaluation, leader/term idempotency, bounded dispatch, and failure-to-task updates.
- `internal/backup/schedule.go`: `NextInTimezone`, preserving strict-after semantics.
- `internal/control/raft.go`: durable `ClaimBackupFire` helper returning the FSM's existing/new run identity.
- `internal/agent/backup_scheduler.go`, `rpc.go`, and `run.go`: Raft control-plane adapters, run creation/task updates, local dispatch, and legacy local-schedule fallback.

Tests/checks:

- `go test ./internal/backup -run 'TestCoordinator_|TestClusterSchedule_' -count=1` passed.
- `go test ./internal/agent -run TestAgentBackupSchedulerResolvesFrozenTargets -count=1` passed.
- Regression coverage added for Raft lease takeover (`created=false`) and delayed ticks catching the current due fire.
- Focused control/backup/agent tests passed.
- `go vet ./internal/backup ./internal/agent ./internal/control` passed.
- `go test ./internal/agent -count=1` reached the existing Playwright freshness failure (`TestP5_Playwright_LoginListFreshness409`); unrelated baseline failure.

Known limitation: the coordinator currently executes local-node tasks directly. Remote Agent task RPC dispatch is left as an explicit `UNAVAILABLE` task result until the execution-plane task RPC is implemented.
