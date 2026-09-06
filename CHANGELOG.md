# Changelog

All notable changes to ProcMesh will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add the offline `procmesh-agent --data-dir DIR --reset-node-identity` maintenance mode for rotating an uninitialized Agent's node ID and discarding an abandoned pending Join without deleting process-plane data. The command refuses running, initialized, or Raft-backed Agents.

### Fixed

- Prevent Raft Leader churn when an older Agent has persisted a wildcard peer address such as `0.0.0.0:18685` or `[::]:18685`. New joins reject non-dialable Raft advertise addresses before consuming a token, the transport refuses unsafe legacy peer dials, and membership reconciliation reports those entries as blocked instead of admitting them.

### Added

#### Resource Metrics Collection (2026-08-16)

ProcMesh Agent now automatically collects and reports resource metrics for nodes and processes:

**Node-level metrics:**
- CPU usage percentage (system-wide)
- Memory usage percentage and total memory
- Disk usage percentage for the data directory partition

**Process-level metrics:**
- Per-process CPU usage percentage
- Per-process memory usage (RSS in bytes)

**Platform support:**
- ✅ Linux (production environment, uses cgroup v2 when available)
- ✅ macOS (development environment, uses system APIs)
- ❌ Windows (planned for future release)

**Integration points:**
- Metrics are collected every 5 seconds by the embedded Collector
- Node metrics are propagated via Gossip to all cluster members
- Process metrics are available through the ConnectRPC `MetricsService` API
- Metrics degrade gracefully: returns `-1` when unavailable (collector stopped or collection failed)

**Disk protection:**
When disk usage exceeds thresholds, the Agent automatically protects itself:
- ≥85%: Warning logged to audit log
- ≥90%: Aggressive cleanup of old process logs
- ≥95%: Stop writing new logs to preserve config/store/raft data

**API endpoints:**
- `GetAgentMetrics`: Returns node-level resource metrics and cluster summary
- `GetProcessMetrics`: Returns per-process resource metrics for managed processes

**Related components:**
- `internal/metrics`: Core metrics collection (Collector, NodeMetrics, ProcessMetrics)
- `internal/agent`: Integration with Agent lifecycle and Cluster Gossip
- `internal/api`: ConnectRPC MetricsService implementation
- `internal/cluster`: NodeSummary includes ResourceSummary for Gossip propagation

**Testing:**
- Unit tests: 91.5% coverage in `internal/metrics`
- Integration tests: Collector lifecycle validation
- E2E tests: Full pipeline validation (Agent → Collector → API/Cluster)
