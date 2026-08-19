# Task 3 Report: ClusterBackupService API

Implemented the ClusterBackupService control-plane contract and handlers.

- Added policy, run, task, validation, and destination-health protobuf messages plus the ten RPCs.
- Regenerated Go ConnectRPC and TypeScript bindings with `make proto` and `make proto-ts`.
- Added API authorization for `backup.manage` mutations and `backup.read` queries.
- Added Raft-backed policy writes and run creation, target/revision freezing, run listing, and per-target `UNAVAILABLE` synthesis.
- Added follower forwarding through the Agent RPC forwarder and registered handlers on public and Agent RPC paths.
- Leader forwarding resolves the Raft leader address through admitted control members; it never picks an arbitrary remote member.
- RetryFailedTasks now applies a fenced Raft mutation that resets only FAILED tasks to PENDING.
- Run creation validates explicit, ALL_ADMITTED, and AGENT_GROUP target membership in both API and FSM paths.
- Responses contain metadata only; no snapshot payloads, S3 access keys, secrets, or backup indexes.

Destination health currently reports `UNKNOWN` conservatively because sink credentials and clients are Agent-local; it does not claim reachability.

Verification:

```text
go test ./internal/api -run 'TestClusterBackupAPI_' -count=1
go test ./internal/api -count=1
make proto
make proto-ts
git diff --check
```
