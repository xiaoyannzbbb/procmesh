# ProcMesh v1 Proto Layout

All files in this directory remain in the `procmesh.v1` package. Moving a
declaration between files must not change its fully qualified name, field
numbers, RPC method names, or streaming semantics.

## Dependency direction

- Foundation types: `errors.proto`, `mutation.proto`, `process_types.proto`,
  `cluster_types.proto`, and `backup_types.proto`.
- Public domain protocols: `access.proto`, `alert.proto`, `audit.proto`,
  `auth.proto`, `backup.proto`, `batch.proto`, `cluster.proto`,
  `cluster_backup.proto`, `disaster_replication.proto`, `metrics.proto`,
  `process.proto`, and `update.proto`.
- Agent-only protocols: `cluster_backup_agent.proto` and
  `peer_replication.proto`.

Foundation files contain only types shared by multiple domains. Public files
must not import Agent-only protocols. Agent-only handlers still revalidate
credentials and authorization at the node that performs the operation; being
excluded from Web generation is not an authorization mechanism.

## Generation

Run `make proto` after changing any Proto file. Run `make proto-ts` when a
public protocol or one of its dependencies changes. Go and Connect bindings
are generated for every file, while TypeScript bindings intentionally exclude
Agent-only protocols.

Web code imports generated domain files and `web/src/lib/rpc/*` adapters
directly. Do not add an `api.proto`, generated-binding barrel, or runtime RPC
barrel that re-aggregates all domains into the initial browser bundle.
