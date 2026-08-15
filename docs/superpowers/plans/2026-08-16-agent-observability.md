# ProcMesh Agent Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add concise, configurable text/JSON operational logs to `procmesh-agent`, with INFO lifecycle events and DEBUG HTTP access records.

**Architecture:** `internal/logging` constructs standard-library `slog` loggers from validated CLI values. The command injects the logger through `agent.Options`; Agent runtime derives component loggers and passes the HTTP component into `api.Options`. Nil loggers become discard loggers so library callers and existing tests remain isolated.

**Tech Stack:** Go 1.26, standard library `log/slog`, Gin, existing Go test suites.

## Global Constraints

- Default format is `text`; `--log-format` accepts case-insensitive `text` or `json`.
- Default level is `info`; `--log-level` accepts case-insensitive `debug`, `info`, `warn`, or `error`.
- Logs go to stderr. No file logging or third-party logging dependency is added.
- INFO includes Agent lifecycle and services that actually listen. HTTP request logs appear only at DEBUG.
- HTTP request fields are exactly `method`, `path`, `status`, `duration_ms`, and `remote_addr`; `path` excludes the query string.
- Managed process stdout/stderr paths and behavior do not change.
- Gossip and Raft library-internal logs remain suppressed.
- Terminal errors are logged once by `cmd/procmesh-agent`; runtime code does not duplicate them.
- Preserve unrelated worktree changes and do not add `docs/superpowers/v1.0-case-coverage-report.md` to any commit.

---

### Task 1: Logger Construction And CLI Flags

**Files:**
- Create: `internal/logging/logging.go`
- Create: `internal/logging/logging_test.go`
- Modify: `cmd/procmesh-agent/main.go`
- Create: `cmd/procmesh-agent/main_test.go`

**Interfaces:**
- Produces: `func logging.New(w io.Writer, format, level string) (*slog.Logger, error)`.
- Produces: `func run(args []string, stderr io.Writer) int` in package `main`; `main` calls it with `os.Args[1:]` and `os.Stderr`.
- Consumes later: `agent.Options.Logger *slog.Logger`, added in Task 2. Until Task 2 lands, Task 1 must add this field as the narrow compilation bridge without changing Agent behavior.

- [ ] **Step 1: Write failing logger tests**

Create table-driven tests using `bytes.Buffer` and literal expectations:

```go
func TestNew_TextAndLevelFiltering(t *testing.T) {
	var out bytes.Buffer
	logger, err := New(&out, "text", "info")
	if err != nil { t.Fatal(err) }
	logger.Debug("hidden")
	logger.Info("agent started", "data_dir", "/data")
	got := out.String()
	if strings.Contains(got, "hidden") { t.Fatalf("debug leaked: %q", got) }
	if !strings.Contains(got, `level=INFO`) || !strings.Contains(got, `msg="agent started"`) || !strings.Contains(got, `data_dir=/data`) {
		t.Fatalf("text log missing fields: %q", got)
	}
}

func TestNew_JSONAndCaseInsensitiveValues(t *testing.T) {
	var out bytes.Buffer
	logger, err := New(&out, "JSON", "DEBUG")
	if err != nil { t.Fatal(err) }
	logger.Debug("http request", "status", 204)
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil { t.Fatal(err) }
	if got["level"] != "DEBUG" || got["msg"] != "http request" || got["status"] != float64(204) {
		t.Fatalf("json log = %#v", got)
	}
}

func TestNew_RejectsUnsupportedValues(t *testing.T) {
	for _, tc := range []struct{ format, level string }{{"yaml", "info"}, {"text", "trace"}} {
		if _, err := New(io.Discard, tc.format, tc.level); err == nil {
			t.Fatalf("New(%q, %q) succeeded", tc.format, tc.level)
		}
	}
}
```

- [ ] **Step 2: Run the logger tests and verify RED**

Run: `go test ./internal/logging -count=1`

Expected: FAIL because package `internal/logging` and `New` do not exist.

- [ ] **Step 3: Implement the minimal logger constructor**

Implement `New` with `strings.ToLower`, a `slog.Level` switch, and either
`slog.NewTextHandler` or `slog.NewJSONHandler`. Return errors containing
`log format` or `log level` plus the rejected value. Treat a nil writer as
an error rather than panicking.

- [ ] **Step 4: Run the logger tests and verify GREEN**

Run: `go test ./internal/logging -count=1`

Expected: PASS.

- [ ] **Step 5: Write failing command tests**

Add these behavioral tests against the wished-for `run` API before refactoring
the command or implementing its logging flags:

```go
func TestRun_InvalidLogFormatExitsTwo(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--data-dir", t.TempDir(), "--log-format", "yaml"}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "log format") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRun_InvalidLogLevelExitsTwo(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--data-dir", t.TempDir(), "--log-level", "trace"}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "log level") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
```

- [ ] **Step 6: Run the command tests and verify RED**

Run: `go test ./cmd/procmesh-agent -count=1`

Expected: FAIL because `run` or its logging flags are missing.

- [ ] **Step 7: Implement command parsing and logger injection**

Use a dedicated `flag.FlagSet` with `flag.ContinueOnError` and its output set
to `stderr`. Register existing flags unchanged plus:

```go
logFormat := fs.String("log-format", "text", "log format: text or json")
logLevel := fs.String("log-level", "info", "log level: debug, info, warn, or error")
```

After required argument checks, call `logging.New(stderr, *logFormat,
*logLevel)`. On validation failure, print the error and return 2 without
calling `agent.Run`. Pass the logger as `agent.Options.Logger`. If `agent.Run`
returns an error, emit exactly one `logger.Error("agent stopped", "error",
err)` and return 1. Return 0 after graceful completion. Keep `main` as:

```go
func main() { os.Exit(run(os.Args[1:], os.Stderr)) }
```

- [ ] **Step 8: Run focused and package tests**

Run: `go test ./internal/logging ./cmd/procmesh-agent -count=1`

Expected: PASS.

- [ ] **Step 9: Commit Task 1**

```bash
git add internal/logging cmd/procmesh-agent internal/agent/run.go
git commit -m "feat: add procmesh-agent logging flags"
```

---

### Task 2: Agent Lifecycle And Component Events

**Files:**
- Modify: `internal/agent/run.go`
- Modify: `internal/agent/run_test.go`
- Modify: `internal/agent/rpc.go`
- Modify: `internal/agent/raft.go`

**Interfaces:**
- Consumes: `agent.Options.Logger *slog.Logger` from Task 1.
- Produces: Agent lifecycle records and component records through the injected logger.
- Produces: `rpcRuntime.logger *slog.Logger`, used by RPC and Raft listener paths.
- Consumes later: an HTTP component logger passed as `api.Options.Logger` in Task 3; Task 2 adds the field as a narrow compilation bridge if needed.

- [ ] **Step 1: Write the failing lifecycle test**

Add one integration-style test that runs the real Agent on ephemeral HTTP,
Gossip, RPC, and Control ports with a text DEBUG logger. Wait for `OnListen`,
cancel, wait for `Run` to return, then assert literal lifecycle messages and
effective addresses:

```go
func TestRun_LogsLifecycle(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	listened := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			DataDir: dir, Listen: "127.0.0.1:0", GossipListen: "127.0.0.1:0",
			RPCListen: "127.0.0.1:0", ControlListen: "127.0.0.1:0",
			Logger: logger, OnListen: func(addr string) { listened <- addr },
		})
	}()
	var httpAddr string
	select {
	case httpAddr = <-listened:
	case err := <-errCh: t.Fatalf("Run exited early: %v", err)
	case <-time.After(5 * time.Second): t.Fatal("timeout waiting for listen")
	}
	cancel()
	select {
	case err := <-errCh: if err != nil { t.Fatal(err) }
	case <-time.After(5 * time.Second): t.Fatal("Run did not stop")
	}
	got := out.String()
	for _, want := range []string{"agent starting", "gossip listening", "http listening", "agent started", "agent stopping", "agent stopped", httpAddr} {
		if !strings.Contains(got, want) { t.Errorf("missing %q in %q", want, got) }
	}
}
```

The production mutation caught by this test is removal of any lifecycle
emission or logging the configured `:0` value instead of the effective bound
HTTP address.

- [ ] **Step 2: Run the lifecycle test and verify RED**

Run: `go test ./internal/agent -run TestRun_LogsLifecycle -count=1`

Expected: FAIL because Agent runtime does not emit lifecycle records.

- [ ] **Step 3: Add injected component loggers and lifecycle events**

At the beginning of `Run`, replace nil `Options.Logger` with a discard-backed
logger. Emit `agent starting` with `data_dir` after required option validation.
Derive `component=gossip`, `component=http`, `component=rpc`, and
`component=raft` loggers.

Emit INFO records only after binds succeed:

```text
gossip listening address=<effective address>
http listening address=<effective address>
rpc listening address=<effective advertised address>
raft control listening address=<effective advertised address>
agent started
```

Add the root/component logger to `rpcRuntime`. Log `rpc serve` failures as
ERROR because they happen asynchronously and cannot propagate to the command.
Log the RPC and Raft listener events after their callbacks would observe a
successful bind.

On context cancellation emit `agent stopping` with `reason` equal to
`ctx.Err().Error()`, perform existing bounded shutdown, then emit `agent
stopped`. Do not emit these graceful records on terminal startup failure.

- [ ] **Step 4: Convert recoverable direct stderr output**

Replace direct Agent stderr writes with WARN records while preserving control
flow. Use stable message names and structured errors:

- `store quarantine failed`, fields `error`, `open_error`
- `store quarantined`, fields `path`, `error`
- `store reopen failed`, field `error`
- `store integrity check failed`, field `error`
- `process recovery failed`, field `error`
- `process reconcile failed`, field `error`
- `cluster bundle load failed`, field `error`
- `gossip rejoin failed`, field `error`
- `disk protection failed`, field `error`
- `insecure listen`, field `address`

Keep `CheckListen(addr, insecure) error` compatible and side-effect free. Log
the insecure warning from `Run` after each successful non-loopback validation,
so validation callers do not need a logger.

When persisted Gossip seeds join successfully, emit INFO `gossip rejoined`
with integer `members` and integer `seeds`.

- [ ] **Step 5: Run focused Agent tests and verify GREEN**

Run: `go test ./internal/agent -run 'TestRun_LogsLifecycle|TestCheckListen|TestRun_BlocksUntilCancelAndServesHealthz' -count=1`

Expected: PASS and no default test log noise.

- [ ] **Step 6: Run the complete Agent suite**

Run: `go test ./internal/agent -count=1 -timeout=240s`

Expected: PASS.

- [ ] **Step 7: Commit Task 2**

```bash
git add internal/agent
git commit -m "feat: log procmesh-agent lifecycle"
```

---

### Task 3: DEBUG HTTP Access Logs

**Files:**
- Modify: `internal/api/server.go`
- Modify: `internal/api/server_test.go`
- Modify: `internal/agent/run.go`

**Interfaces:**
- Consumes: `api.Options.Logger *slog.Logger`, passed by Agent as a logger with `component=http`.
- Produces: one DEBUG `http request` record after every completed Gin request.

- [ ] **Step 1: Write failing access-log tests**

Add tests using real Gin request handling and JSON slog output:

```go
func TestServer_DebugLogsHTTPAccessWithoutQuery(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv, err := NewServer(Options{Started: time.Now(), Logger: logger})
	if err != nil { t.Fatal(err) }
	req := httptest.NewRequest(http.MethodGet, "/healthz?token=secret", nil)
	req.RemoteAddr = "192.0.2.1:4321"
	rec := httptest.NewRecorder()
	srv.Engine.ServeHTTP(rec, req)
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil { t.Fatal(err) }
	if got["msg"] != "http request" || got["method"] != "GET" || got["path"] != "/healthz" || got["status"] != float64(200) || got["remote_addr"] != "192.0.2.1:4321" {
		t.Fatalf("access log = %#v", got)
	}
	if _, ok := got["duration_ms"].(float64); !ok { t.Fatalf("duration_ms = %#v", got["duration_ms"]) }
	if strings.Contains(out.String(), "secret") { t.Fatalf("query leaked: %q", out.String()) }
}

func TestServer_InfoSuppressesHTTPAccess(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv, err := NewServer(Options{Started: time.Now(), Logger: logger})
	if err != nil { t.Fatal(err) }
	srv.Engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if out.Len() != 0 { t.Fatalf("INFO access output = %q", out.String()) }
}
```

The production mutations caught are logging at INFO, including `RawQuery`,
omitting a required field, or logging before the final response status exists.

- [ ] **Step 2: Run access-log tests and verify RED**

Run: `go test ./internal/api -run 'TestServer_(DebugLogsHTTPAccessWithoutQuery|InfoSuppressesHTTPAccess)' -count=1`

Expected: FAIL because `api.Options.Logger` or middleware output is missing.

- [ ] **Step 3: Implement access middleware**

Add `Logger *slog.Logger` to `api.Options`. If nil, use a discard logger. Install
one middleware immediately after `gin.New()` and before route registration:

```go
func accessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		logger.Debug("http request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(started).Milliseconds(),
			"remote_addr", c.Request.RemoteAddr,
		)
	}
}
```

Pass `logger.With("component", "http")` from `agent.Run` into
`api.NewServer`. Do not log request or response bodies, headers, or query
strings.

- [ ] **Step 4: Run API and Agent tests and verify GREEN**

Run: `go test ./internal/api ./internal/agent -count=1 -timeout=240s`

Expected: PASS.

- [ ] **Step 5: Commit Task 3**

```bash
git add internal/api internal/agent/run.go
git commit -m "feat: add debug HTTP access logs"
```

---

### Task 4: End-To-End Verification And Documentation Alignment

**Files:**
- Modify only if verification exposes a real defect in files already touched by Tasks 1-3.

**Interfaces:**
- Consumes: completed logging CLI, lifecycle events, and access middleware.
- Produces: verified binaries and evidence that text/JSON modes behave as specified.

- [ ] **Step 1: Run formatting and static checks**

Run: `gofmt -w cmd/procmesh-agent/*.go internal/logging/*.go internal/agent/*.go internal/api/*.go`

Run: `go vet ./cmd/procmesh-agent ./internal/logging ./internal/agent ./internal/api`

Expected: both commands exit 0.

- [ ] **Step 2: Run the full Go test suite**

Run: `go test ./... -count=1 -timeout=300s`

Expected: PASS.

- [ ] **Step 3: Build all binaries**

Run: `go build ./cmd/procmesh-agent ./cmd/procmesh ./cmd/procmesh-shim`

Expected: exit 0.

- [ ] **Step 4: Verify text INFO output manually**

Build a temporary Agent binary, start it with a temporary data directory and
all four listeners on `127.0.0.1:0`, wait for `agent started`, terminate it,
and assert the captured stderr contains readable key-value records for
`agent starting`, `gossip listening`, `http listening`, `agent started`, and
`agent stopped`. Assert it contains no `http request` record.

- [ ] **Step 5: Verify JSON DEBUG output manually**

Repeat with `--log-format=json --log-level=debug`, extract the effective HTTP
address from the JSON `http listening` record, request `/healthz?token=secret`,
terminate the Agent, and parse every non-empty stderr line as JSON. Assert one
`http request` record contains path `/healthz`, status `200`, all five required
fields, and no occurrence of `secret`.

- [ ] **Step 6: Commit verification fixes if any**

If and only if Steps 1-5 required code corrections, commit only those focused
corrections:

```bash
git add cmd/procmesh-agent internal/logging internal/agent internal/api
git commit -m "fix: align agent logging verification"
```
