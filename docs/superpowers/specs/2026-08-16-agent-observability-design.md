# ProcMesh Agent Observability Design

## Goal

Give `procmesh-agent` useful, configurable operational logs while keeping the
default output concise. Logs are written to stderr so they work naturally in a
terminal and are collected by systemd's journal without changing the service
unit.

This design covers logs produced by the Agent itself. Managed process stdout
and stderr remain in
`<data-dir>/logs/<process-id>/<instance-id>/{stdout,stderr}.log`.

## Command-Line Interface

`procmesh-agent` adds two flags:

- `--log-format=text`: accepts `text` or `json`.
- `--log-level=info`: accepts `debug`, `info`, `warn`, or `error`.

Both flags are case-insensitive. An invalid value is a command-line usage error:
the Agent prints a concise message to stderr and exits with status 2 before
opening the data directory or binding a listener.

The default text format is the standard `log/slog` key-value representation,
for example:

```text
time=2026-08-16T12:00:00.000+08:00 level=INFO msg="agent started" data_dir=/var/lib/procmesh
```

JSON format uses the standard `log/slog` JSON handler and retains the same
field names and levels.

## Architecture

### Logger construction

A focused `internal/logging` package owns logger construction and validation.
It exposes a constructor that accepts an `io.Writer`, format, and level, and
returns `(*slog.Logger, error)`. The package has no knowledge of Agent runtime
behavior.

`cmd/procmesh-agent` constructs the logger after flag parsing and passes it to
`agent.Run` through a new `Logger *slog.Logger` field on `agent.Options`.
Production always supplies a logger. A nil logger passed by library callers or
existing tests becomes a discard logger inside `agent.Run`; this preserves
test isolation and prevents unexpected process-wide global logger state.

No third-party logging dependency is added.

### Agent events

Agent runtime code uses the injected logger for operational events. Existing
direct stderr warnings and recoverable errors are converted to structured
records with stable messages and fields.

INFO records cover:

- Agent startup, including `data_dir`.
- Each service that actually begins listening: HTTP, Gossip, RPC, and Raft
  control. Each record includes its effective bound or advertised address.
- Successful cluster rejoin when persisted seeds are present.
- Graceful shutdown, including the shutdown reason.

WARN records cover recoverable conditions such as store quarantine, degraded
integrity, failed recovery/reconciliation, failed persisted-seed rejoin, disk
protection errors, and insecure non-loopback listening.

ERROR is reserved for terminal failures. `agent.Run` returns terminal errors;
`cmd/procmesh-agent` logs the returned error once as `agent stopped` before
exiting with status 1. Runtime code must not also log the same terminal error.

Gossip and Raft library-internal logs remain suppressed. The Agent records
their meaningful lifecycle outcomes itself. This avoids exposing probe,
heartbeat, election, and transport chatter at the default INFO level and avoids
maintaining adapters between incompatible logging interfaces in this change.

### HTTP access logs

The API server receives the Agent logger and installs one middleware around all
routes. The middleware records one DEBUG event after each request completes
with these fields:

- `method`
- `path` (URL path only, excluding the query string)
- `status`
- `duration_ms`
- `remote_addr`

The message is `http request`. Health and metrics requests follow the same rule;
they are invisible at the default INFO level. Query strings are excluded to
avoid logging credentials or other sensitive values.

## Data Flow

1. The command parses logging flags.
2. `internal/logging` validates them and creates a stderr-backed `slog.Logger`.
3. The command logs terminal startup failures and passes the logger to
   `agent.Run`.
4. `agent.Run` derives component loggers with a `component` field and passes the
   API logger into `api.NewServer`.
5. Runtime events and DEBUG request records flow through the same handler, so
   format and level filtering are consistent.

The initial startup record is emitted by `agent.Run`, after basic option
validation but before filesystem initialization. Listener records are emitted
only after the relevant bind succeeds. The final graceful-shutdown record is
emitted after HTTP, RPC, Raft, and Gossip shutdown has been attempted.

## Error Handling

- Invalid logging configuration exits with status 2 and never calls
  `agent.Run`.
- A logging writer error is handled by `log/slog` according to its standard
  handler behavior and does not crash the Agent.
- Errors that already propagate to the command are returned without a second
  runtime ERROR record.
- Shutdown errors remain best-effort because the existing lifecycle already
  treats shutdown as bounded cleanup. The shutdown record states completion;
  individual cleanup errors are WARN records when available.

## Testing

Tests use `bytes.Buffer` outputs and real `slog` handlers; they do not replace
the logger with mocks.

Coverage includes:

- Text output contains the message, level, and structured fields.
- JSON output parses as JSON and contains equivalent fields.
- INFO suppresses DEBUG while DEBUG emits it.
- Unsupported format and level values return validation errors.
- Agent startup and graceful cancellation emit lifecycle records.
- An HTTP request emits method, path, status, duration, and remote address at
  DEBUG.
- The same HTTP request produces no access record at the default INFO level.
- Existing Agent and API suites continue to pass with nil/discard loggers.

Manual verification starts the built `procmesh-agent` on ephemeral ports in
both formats, calls `/healthz`, and confirms that text is readable, JSON lines
parse, INFO stays concise, and DEBUG contains the request record.

## Compatibility And Scope

- Existing command lines continue to work because both new flags have defaults.
- The systemd unit needs no change because stderr is already captured by the
  journal for a simple service.
- Managed process log paths and rotation behavior do not change.
- The change does not add file logging, remote log shipping, log sampling,
  dynamic level reload, request or response bodies, or direct Gossip/Raft
  library debug output.
