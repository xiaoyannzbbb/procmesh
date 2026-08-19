# Task 3 Report: ClusterBackupService API

Implemented the ClusterBackupService control-plane contract and handlers.

- Added policy, run, task, validation, and destination-health protobuf messages plus the ten RPCs.
- Regenerated Go ConnectRPC and TypeScript bindings with `make proto` and `make proto-ts`.
- Added API authorization for `backup.manage` mutations and `backup.read` queries.
- Added Raft-backed policy writes and run creation, target/revision freezing, run listing, and per-target `UNAVAILABLE` synthesis.
- Added follower forwarding through the Agent RPC forwarder and registered handlers on public and Agent RPC paths.
- Responses contain metadata only; no snapshot payloads, S3 access keys, secrets, or backup indexes.

Verification:

```text
go test ./internal/api -run 'TestClusterBackupAPI_' -count=1
go test ./internal/api -count=1
make proto
make proto-ts
git diff --check
```
