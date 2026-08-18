# 进程日志目录可配置 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 每个进程可配置日志目录和 stderr 合并到 stdout；改配置不自动重启，重启前仍读当前在写的文件，并提示重启后生效。

**Architecture:** 在 `logmgr` 增加单一路径解析与目录校验。Start 用最新 Spec；tail / stream / download / 轮转用 `active_revision` 对应的 Spec。`log_path_pending` 只在 `ProcessView.Instance` 上计算。磁盘自动删除仅额外扫描与 data-dir 同一分区的自定义目录，且只删 `{directory}/{ordinal}/` 下的本 Agent 日志文件。

**Tech Stack:** Go 1.25、ConnectRPC/protobuf、SQLite revision 快照、Vue 3 Composition API、TypeScript、i18next。

**Spec:** `docs/superpowers/specs/2026-08-18-process-log-directory-design.md`

## Global Constraints

- 默认路径必须保持 `<data-dir>/logs/<process_id>/<instance_id>/{stdout,stderr}.log`。
- 自定义目录：`{directory}/{ordinal}/stdout.log`（以及未重定向时的 `stderr.log`）。
- `redirect_stderr` 只影响写入（shim 两边同一路径）；读 `stream=stderr` 仍读 `stderr.log`，没有就是空。
- 校验失败消息必须以 `log path: ` 开头；`Error()` 为 `INVALID: log path: ...`。
- 不改 shim 协议；不自动搬家；不因改目录自动重启。
- 自动删除阈值用配置的 `cleanup_percent`，不是写死 90%。
- 前端文案必须中英双语；改完跑 `cd web && npm run i18n:check`。
- 新 protobuf 字段只能追加：`LogPolicy.directory=5`、`LogPolicy.redirect_stderr=6`、`Instance.log_path_pending=12`。

## File Map

```text
internal/logmgr/paths.go                 # Resolve / WritePaths / ValidateDirectory / SameDevice
internal/logmgr/logmgr.go                # InstancePaths 改为调 Resolve；Protect 扫 ExtraLogDirs
internal/logmgr/logmgr_test.go           # 路径、校验、同分区删除测试
internal/process/types.go                # LogPolicy.Directory / RedirectStderr
internal/process/validate.go             # ValidateSpec 调 ValidateDirectory
internal/process/validate_test.go        # 非法目录文案
internal/process/logpath.go              # EffectiveLog / LogPathPending / 写路径
internal/process/logpath_test.go         # pending / effective 回退
internal/process/manager.go              # Apply 时带 data-dir 再验；Start/Rotate 用 Resolve
internal/process/reconcile.go            # Start 用最新 Spec 写路径
internal/process/manager_test.go         # 自定义目录启动、pending、重定向
proto/procmesh/v1/api.proto              # 三个新字段
internal/api/convert.go                  # Spec/View 映射
internal/api/log.go                      # tail/stream/download 用 effective 路径
internal/api/process.go                  # viewOf 填 log_path_pending
internal/api/batch_expand.go             # applyNonEmptyLog
internal/cli/specfile.go                 # YAML directory / redirect_stderr
internal/cli/process.go                  # process get 打印 pending
internal/localhttp/dto.go                # DTO 字段
internal/localhttp/server.go             # DTO 映射 + /logs 用 effective 路径
internal/agent/run.go                    # Protect 前注入 ExtraLogDirs
web/src/pages/processConfigForm.ts       # 表单字段与客户端绝对路径校验
web/src/pages/processConfigSchema.ts     # 表单 schema
web/src/pages/processView.ts             # logPathPending / redirectStderr
web/src/pages/ProcessDetailPage.vue      # pending 横幅
web/src/pages/ProcessConfigPanel.vue     # 保存后 pending 横幅
web/src/pages/ProcessLogsPanel.vue       # stderr 合并提示
web/public/locales/{en,zh}/common.json   # i18n
```

---

### Task 1: 路径解析与目录校验

**Files:**
- Create: `internal/logmgr/paths.go`
- Modify: `internal/logmgr/logmgr.go`（`InstancePaths` 改为调用 `Resolve`）
- Modify: `internal/logmgr/logmgr_test.go`

**Interfaces:**
- Produces: `func Resolve(layout paths.Layout, directory, processID, instanceID string, ordinal int) (stdout, stderr string)`
- Produces: `func WritePaths(stdout, stderr string, redirect bool) (out, errp string)`
- Produces: `func ValidateDirectory(dir, dataRoot string) error`

- [ ] **Step 1: 写失败测试**

在 `internal/logmgr/logmgr_test.go` 追加：

```go
func TestResolve_DefaultAndCustom(t *testing.T) {
	layout := paths.New("/data")
	stdout, stderr := logmgr.Resolve(layout, "", "p1", "p1:0", 0)
	if stdout != "/data/logs/p1/p1:0/stdout.log" || stderr != "/data/logs/p1/p1:0/stderr.log" {
		t.Fatalf("default %q %q", stdout, stderr)
	}
	stdout, stderr = logmgr.Resolve(layout, "/var/log/myapp", "p1", "p1:0", 1)
	if stdout != "/var/log/myapp/1/stdout.log" || stderr != "/var/log/myapp/1/stderr.log" {
		t.Fatalf("custom %q %q", stdout, stderr)
	}
}

func TestWritePaths_Redirect(t *testing.T) {
	out, errp := logmgr.WritePaths("/a/stdout.log", "/a/stderr.log", true)
	if out != "/a/stdout.log" || errp != "/a/stdout.log" {
		t.Fatalf("got %q %q", out, errp)
	}
}

func TestValidateDirectory(t *testing.T) {
	root := "/var/lib/procmesh"
	cases := []struct {
		dir, root, want string
	}{
		{"", "", ""},
		{"/var/log/myapp", root, ""},
		{"rel", "", "log path: directory must be an absolute path"},
		{"./foo", "", "log path: directory must be an absolute path"},
		{"/", "", "log path: directory must not be /"},
		{"/etc/foo", "", "log path: directory is not allowed under /etc"},
		{"/var/log/../etc", "", "log path: directory is not allowed under /etc"},
		{"/etcfoo", "", ""},
		{root + "/raft/x", root, "log path: directory must not point at Agent internal data (store.db, raft, cluster, runtime, shim)"},
		{root + "/logs/app", root, ""},
	}
	for _, tc := range cases {
		err := logmgr.ValidateDirectory(tc.dir, tc.root)
		if tc.want == "" {
			if err != nil {
				t.Fatalf("dir=%q: %v", tc.dir, err)
			}
			continue
		}
		if err == nil || !errcode.Is(err, errcode.INVALID) || err.Error() != "INVALID: "+tc.want {
			t.Fatalf("dir=%q err=%v want %q", tc.dir, err, tc.want)
		}
	}
}
```

`errcode` 包路径：`github.com/qleelulu/procmesh/internal/errcode`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/logmgr -run 'TestResolve_DefaultAndCustom|TestWritePaths_Redirect|TestValidateDirectory' -count=1`

Expected: FAIL，未定义 `Resolve` / `WritePaths` / `ValidateDirectory`。

- [ ] **Step 3: 实现**

创建 `internal/logmgr/paths.go`：

```go
package logmgr

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/paths"
	"golang.org/x/sys/unix"
)

var systemLogDirPrefixes = []string{
	"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64",
	"/boot", "/dev", "/proc", "/sys", "/root",
}

var agentInternalNames = []string{"store.db", "raft", "cluster", "runtime", "shim"}

func Resolve(layout paths.Layout, directory, processID, instanceID string, ordinal int) (stdout, stderr string) {
	var dir string
	if directory == "" {
		dir = filepath.Join(layout.LogDir, processID, instanceID)
	} else {
		dir = filepath.Join(directory, strconv.Itoa(ordinal))
	}
	return filepath.Join(dir, "stdout.log"), filepath.Join(dir, "stderr.log")
}

func WritePaths(stdout, stderr string, redirect bool) (string, string) {
	if redirect {
		return stdout, stdout
	}
	return stdout, stderr
}

func ValidateDirectory(dir, dataRoot string) error {
	if dir == "" {
		return nil
	}
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) || clean == "." {
		return errcode.E(errcode.INVALID, "log path: directory must be an absolute path")
	}
	if clean == "/" {
		return errcode.E(errcode.INVALID, "log path: directory must not be /")
	}
	for _, p := range systemLogDirPrefixes {
		if clean == p || strings.HasPrefix(clean, p+"/") {
			return errcode.E(errcode.INVALID, "log path: directory is not allowed under "+p)
		}
	}
	if dataRoot == "" {
		return nil
	}
	rel, err := filepath.Rel(filepath.Clean(dataRoot), clean)
	if err != nil {
		return nil
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return nil
	}
	first, _, _ := strings.Cut(rel, "/")
	for _, name := range agentInternalNames {
		if first == name {
			return errcode.E(errcode.INVALID, "log path: directory must not point at Agent internal data (store.db, raft, cluster, runtime, shim)")
		}
	}
	return nil
}

func deviceID(path string) (uint64, bool) {
	for p := path; p != "" && p != string(filepath.Separator); p = filepath.Dir(p) {
		var st unix.Stat_t
		if err := unix.Stat(p, &st); err == nil {
			return uint64(st.Dev), true
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return 0, false
}

func SameDevice(a, b string) bool {
	da, oka := deviceID(a)
	db, okb := deviceID(b)
	return oka && okb && da == db
}
```

把 `logmgr.go` 里的 `InstancePaths` 改成：

```go
func InstancePaths(layout paths.Layout, processID, instanceID string) (stdout, stderr string) {
	return Resolve(layout, "", processID, instanceID, 0)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/logmgr -count=1`

Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/logmgr/paths.go internal/logmgr/logmgr.go internal/logmgr/logmgr_test.go
git commit -m "$(cat <<'EOF'
feat(logmgr): resolve and validate per-process log directories

EOF
)"
```

---

### Task 2: Process Spec 字段与保存校验

**Files:**
- Modify: `internal/process/types.go`
- Modify: `internal/process/validate.go`
- Modify: `internal/process/validate_test.go`
- Modify: `internal/process/manager.go`（`ApplySpec` 在 `ValidateSpec` 之后用 `layout.Root` 再验一次）
- Modify: `internal/process/manager_test.go`

**Interfaces:**
- Produces: `process.LogPolicy.Directory string`
- Produces: `process.LogPolicy.RedirectStderr bool`
- `WithDefaults` 不得改这两个字段。

- [ ] **Step 1: 写失败测试**

`internal/process/validate_test.go`：

```go
func TestValidateSpec_LogDirectoryMessages(t *testing.T) {
	s := validSpec()
	s.Log.Directory = "relative"
	err := process.ValidateSpec(s)
	if err == nil || err.Error() != "INVALID: log path: directory must be an absolute path" {
		t.Fatalf("got %v", err)
	}
	s.Log.Directory = "/etc/procmesh"
	err = process.ValidateSpec(s)
	if err == nil || !strings.Contains(err.Error(), "log path:") || !strings.Contains(err.Error(), "/etc") {
		t.Fatalf("got %v", err)
	}
	s.Log.Directory = ""
	if err := process.ValidateSpec(s); err != nil {
		t.Fatal(err)
	}
}
```

`internal/process/manager_test.go` 追加（复用 `newTestManager` / `shortRoot`）：

```go
func TestApplySpec_RejectsLogDirInsideRaft(t *testing.T) {
	ctx := context.Background()
	m, _, layout := newTestManager(t)
	spec := process.ProcessSpec{
		Name: "n", Command: "/bin/true", Instances: 1,
		Log: process.LogPolicy{Directory: filepath.Join(layout.Root, "raft", "logs")},
	}
	_, err := m.ApplySpec(ctx, spec, 0, "op-log-bad", "t", "")
	if err == nil || !strings.Contains(err.Error(), "log path:") {
		t.Fatalf("got %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/process -run 'TestValidateSpec_LogDirectoryMessages|TestApplySpec_RejectsLogDirInsideRaft' -count=1`

Expected: FAIL（字段不存在或校验未执行）。

- [ ] **Step 3: 实现**

`types.go` 的 `LogPolicy`：

```go
type LogPolicy struct {
	MaxSize         int64
	MaxFiles        int
	MaxAge          time.Duration
	Compress        bool
	Directory       string
	RedirectStderr  bool
}
```

`WithDefaults` 保持只填 MaxSize/MaxFiles/MaxAge/Compress。`empty := p == LogPolicy{}` 仍然正确（新字段零值）。

`validate.go` 的 `ValidateSpec` 末尾加：

```go
if err := logmgr.ValidateDirectory(s.Log.Directory, ""); err != nil {
	return err
}
```

`manager.go` `ApplySpec` 在 `ValidateSpec` 成功后：

```go
if err := logmgr.ValidateDirectory(spec.Log.Directory, m.deps.Layout.Root); err != nil {
	return ProcessSpec{}, err
}
```

`RollbackSpec` 写入的是历史已校验过的 Spec，不必再拦系统路径；若历史目录现在落在当前 data-dir 的 `raft/` 下（几乎不可能），保持放行。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/process -run 'TestValidateSpec|TestApplySpec_RejectsLogDirInsideRaft' -count=1`

Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/process/types.go internal/process/validate.go internal/process/validate_test.go internal/process/manager.go internal/process/manager_test.go
git commit -m "$(cat <<'EOF'
feat(process): validate log.directory on spec apply

EOF
)"
```

---

### Task 3: Effective 路径、Start、轮转

**Files:**
- Create: `internal/process/logpath.go`
- Create: `internal/process/logpath_test.go`
- Modify: `internal/process/manager.go`（`StateStore` 增加 `GetRevisionSpec`；`RotateLogs` 用 effective）
- Modify: `internal/process/reconcile.go`
- Modify: `internal/process/manager_test.go`

**Interfaces:**
- Produces: `func (m *Manager) EffectiveLog(ctx context.Context, spec ProcessSpec, inst Instance) LogPolicy`
- Produces: `func LogPathPending(latest, active LogPolicy, inst Instance) bool`
- Produces: `func (m *Manager) ReadLogPath(ctx context.Context, spec ProcessSpec, inst Instance, stream string) string`
- Produces: `func (m *Manager) CustomLogDirs(ctx context.Context) []string`

- [ ] **Step 1: 写失败测试**

`internal/process/logpath_test.go`：

```go
func TestLogPathPending(t *testing.T) {
	inst := process.Instance{ActiveRevision: 1}
	if process.LogPathPending(process.LogPolicy{}, process.LogPolicy{}, process.Instance{}) {
		t.Fatal("never started")
	}
	if process.LogPathPending(process.LogPolicy{Directory: "/var/log/a"}, process.LogPolicy{Directory: "/var/log/a"}, inst) {
		t.Fatal("same")
	}
	if !process.LogPathPending(process.LogPolicy{Directory: "/var/log/b"}, process.LogPolicy{Directory: "/var/log/a"}, inst) {
		t.Fatal("dir changed")
	}
	if !process.LogPathPending(process.LogPolicy{RedirectStderr: true}, process.LogPolicy{}, inst) {
		t.Fatal("redirect changed")
	}
}
```

`manager_test.go` 追加（可参考 `TestReconcile_WritesStdoutToInstanceLog`）：

```go
func TestReconcile_CustomDirectoryAndRedirect(t *testing.T) {
	ctx := context.Background()
	m, st, layout := newTestManager(t)
	t.Cleanup(func() { killManaged(t, st, "p1") })
	dir := filepath.Join(t.TempDir(), "app")
	spec := process.ProcessSpec{
		ProcessID: "p1", Name: "echo", Command: "/bin/sh",
		Args: []string{"-c", "printf 'out\\n'; printf 'err\\n' >&2; exec sleep 60"},
		Instances: 1,
		Log: process.LogPolicy{Directory: dir, RedirectStderr: true},
	}
	if _, err := m.ApplySpec(ctx, spec, 0, "op-c", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	stdout := filepath.Join(dir, "0", "stdout.log")
	stderr := filepath.Join(dir, "0", "stderr.log")
	waitFileContains(t, stdout, "out")
	if _, err := os.Stat(stderr); !os.IsNotExist(err) {
		t.Fatal("stderr.log must not exist when redirected")
	}
	body, err := os.ReadFile(stdout)
	if err != nil || !strings.Contains(string(body), "err") {
		t.Fatalf("merged stderr missing: %q %v", body, err)
	}
	_ = layout
}

func TestRotateLogs_UsesActiveRevisionPath(t *testing.T) {
	ctx := context.Background()
	m, st, layout := newTestManager(t)
	t.Cleanup(func() { killManaged(t, st, "p1") })
	oldDir := filepath.Join(t.TempDir(), "old")
	spec := process.ProcessSpec{
		ProcessID: "p1", Name: "n", Command: "/bin/sleep", Args: []string{"60"},
		Instances: 1, Log: process.LogPolicy{Directory: oldDir, MaxSize: 32, MaxFiles: 2},
	}
	got, err := m.ApplySpec(ctx, spec, 0, "op1", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetDesired(ctx, "p1", process.DesiredRunning, "op-s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	oldStdout := filepath.Join(oldDir, "0", "stdout.log")
	if err := os.WriteFile(oldStdout, bytes.Repeat([]byte("x"), 64), 0o640); err != nil {
		t.Fatal(err)
	}
	newDir := filepath.Join(t.TempDir(), "new")
	got.Log.Directory = newDir
	if _, err := m.ApplySpec(ctx, got, got.LatestRevision, "op2", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.RotateLogs(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(oldStdout)
	if err != nil || info.Size() != 0 {
		t.Fatalf("should rotate old file, size err=%v info=%v", err, info)
	}
	if _, err := os.Stat(filepath.Join(newDir, "0", "stdout.log")); !os.IsNotExist(err) {
		t.Fatal("must not create new path before restart")
	}
	_ = layout
	_ = st
}
```

若仓库没有 `waitFileContains`，用短轮询：

```go
func waitFileContains(t *testing.T, path, sub string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), sub) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %q in %s", sub, path)
}
```

两实例路径断言可并入第一个测试，把 `Instances` 改 2 另开一个更小的测试：

```go
func TestResolve_TwoInstancesUseOrdinalDirs(t *testing.T) {
	layout := paths.New("/data")
	a, _ := logmgr.Resolve(layout, "/var/log/app", "p", "p:0", 0)
	b, _ := logmgr.Resolve(layout, "/var/log/app", "p", "p:1", 1)
	if a != "/var/log/app/0/stdout.log" || b != "/var/log/app/1/stdout.log" {
		t.Fatalf("%s %s", a, b)
	}
}
```

（若 Task 1 已覆盖，不必重复。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/process -run 'TestLogPathPending|TestReconcile_CustomDirectoryAndRedirect|TestRotateLogs_UsesActiveRevisionPath' -count=1`

Expected: FAIL。

- [ ] **Step 3: 实现**

`manager.go` 的 `StateStore` 增加：

```go
GetRevisionSpec(ctx context.Context, processID string, rev int64) (ProcessSpec, error)
```

`*store.Store` 已有该方法。

`internal/process/logpath.go`：

```go
package process

import (
	"context"

	"github.com/qleelulu/procmesh/internal/logmgr"
)

func LogPathPending(latest, active LogPolicy, inst Instance) bool {
	if inst.ActiveRevision == 0 {
		return false
	}
	return latest.Directory != active.Directory || latest.RedirectStderr != active.RedirectStderr
}

func (m *Manager) EffectiveLog(ctx context.Context, spec ProcessSpec, inst Instance) LogPolicy {
	if inst.ActiveRevision <= 0 || inst.ActiveRevision == spec.LatestRevision {
		return spec.Log
	}
	old, err := m.deps.Store.GetRevisionSpec(ctx, spec.ProcessID, inst.ActiveRevision)
	if err != nil {
		return spec.Log
	}
	return old.Log
}

func (m *Manager) ReadLogPath(ctx context.Context, spec ProcessSpec, inst Instance, stream string) string {
	pol := m.EffectiveLog(ctx, spec, inst)
	stdout, stderr := logmgr.Resolve(m.deps.Layout, pol.Directory, spec.ProcessID, inst.InstanceID, inst.Ordinal)
	if stream == "stderr" {
		return stderr
	}
	return stdout
}

func (m *Manager) writeLogPaths(spec ProcessSpec, inst Instance) (stdout, stderr string) {
	stdout, stderr = logmgr.Resolve(m.deps.Layout, spec.Log.Directory, spec.ProcessID, inst.InstanceID, inst.Ordinal)
	return logmgr.WritePaths(stdout, stderr, spec.Log.RedirectStderr)
}

func (m *Manager) CustomLogDirs(ctx context.Context) []string {
	specs, err := m.deps.Store.ListSpecs(ctx)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, s := range specs {
		d := s.Log.Directory
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}
```

`reconcile.go` 把：

```go
stdout, stderr := logmgr.InstancePaths(m.deps.Layout, spec.ProcessID, inst.InstanceID)
```

换成：

```go
stdout, stderr := m.writeLogPaths(spec, *inst)
```

`redirect_stderr` 时 `Prepare` 只传 stdout（`WritePaths` 已让两边相同，`Prepare` 对同一路径调用两次也无害；更干净的写法：若 `stdout == stderr` 则 `Prepare(stdout)`）。

紧急停写分支保持两边 `/dev/null`。

`RotateLogs` 内层循环改成：

```go
pol := m.EffectiveLog(ctx, spec, inst)
lp := pol.WithDefaults()
rpol := logmgr.RotatePolicy{MaxSize: lp.MaxSize, MaxFiles: lp.MaxFiles, MaxAge: lp.MaxAge, Compress: lp.Compress}
stdout, stderr := logmgr.Resolve(m.deps.Layout, pol.Directory, spec.ProcessID, inst.InstanceID, inst.Ordinal)
if err := logmgr.Rotate(stdout, rpol, now); err != nil && first == nil {
	first = err
}
if stderr != stdout {
	if err := logmgr.Rotate(stderr, rpol, now); err != nil && first == nil {
		first = err
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/process -count=1`

Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/process/logpath.go internal/process/logpath_test.go internal/process/manager.go internal/process/reconcile.go internal/process/manager_test.go
git commit -m "$(cat <<'EOF'
feat(process): start and rotate using configurable log directories

EOF
)"
```

---

### Task 4: Protobuf 与 API / CLI / localhttp 映射

**Files:**
- Modify: `proto/procmesh/v1/api.proto`
- Generated: `proto/procmesh/v1/api.pb.go`、`web/src/gen/procmesh/v1/api_pb.ts`（`make proto` + `make proto-ts`）
- Modify: `internal/api/convert.go`
- Modify: `internal/api/convert_test.go`
- Modify: `internal/api/process.go`
- Modify: `internal/api/log.go`
- Modify: `internal/api/log_test.go`（若已有路径断言则改用 Manager）
- Modify: `internal/api/batch_expand.go`
- Modify: `internal/api/proto_gen_test.go`
- Modify: `internal/cli/specfile.go`
- Modify: `internal/cli/process.go`
- Modify: `internal/localhttp/dto.go`
- Modify: `internal/localhttp/server.go`

- [ ] **Step 1: 改 proto**

`LogPolicy` 追加：

```text
  string directory = 5;
  bool redirect_stderr = 6;
```

`Instance` 追加：

```text
  bool log_path_pending = 12;
```

- [ ] **Step 2: 生成代码**

Run:

```bash
make proto
make proto-ts
```

Expected: 生成文件含 `Directory` / `RedirectStderr` / `LogPathPending`。

`proto_gen_test.go` 增加：

```go
_ = (&procmeshv1.LogPolicy{}).GetDirectory
_ = (&procmeshv1.Instance{}).GetLogPathPending
```

- [ ] **Step 3: 映射与 pending / 读路径**

`convert.go`：

```go
Log: &procmeshv1.LogPolicy{
    MaxSize: s.Log.MaxSize,
    MaxFiles: int32(s.Log.MaxFiles),
    MaxAgeSeconds: durationSeconds(s.Log.MaxAge),
    Compress: s.Log.Compress,
    Directory: s.Log.Directory,
    RedirectStderr: s.Log.RedirectStderr,
},
```

`ProtoToSpec` 同样填 `Directory` / `RedirectStderr`。

`ViewOf` 保持不填 pending（没有 revision 上下文）。

`process.go` `viewOf`：

```go
view := ViewOf(spec, insts)
for i, inst := range insts {
	active := s.Mgr.EffectiveLog(ctx, spec, inst)
	view.Instances[i].LogPathPending = process.LogPathPending(spec.Log, active, inst)
}
return view, nil
```

`log.go` 把 `logPath(layout, processID, instID, stream)` 换成对每个 instance：

```go
inst, err := s.Mgr.GetInstance(ctx, instID) // 若无 GetInstance，用 ListInstances 建 map
path := s.Mgr.ReadLogPath(ctx, spec, inst, stream)
```

`Manager` 已有 `ListInstances`。给 Manager 加薄封装（若还没有）：

```go
func (m *Manager) GetInstance(ctx context.Context, instanceID string) (Instance, error) {
	return m.deps.Store.GetInstance(ctx, instanceID)
}
```

`localhttp` `logsHandler` 同样对每个 inst 调 `s.mgr.ReadLogPath`。

`batch_expand.go` `applyNonEmptyLog`：

```go
if src.GetDirectory() != "" {
	dst.Directory = src.GetDirectory()
}
if src.GetRedirectStderr() {
	dst.RedirectStderr = true
}
```

`cli/specfile.go` `LogPolicyDTO`：

```go
type LogPolicyDTO struct {
	MaxSize        int64  `json:"max_size,omitempty" yaml:"max_size,omitempty"`
	MaxFiles       int    `json:"max_files,omitempty" yaml:"max_files,omitempty"`
	MaxAgeSeconds  int64  `json:"max_age_seconds,omitempty" yaml:"max_age_seconds,omitempty"`
	Compress       bool   `json:"compress,omitempty" yaml:"compress,omitempty"`
	Directory      string `json:"directory,omitempty" yaml:"directory,omitempty"`
	RedirectStderr bool   `json:"redirect_stderr,omitempty" yaml:"redirect_stderr,omitempty"`
}
```

`specToProto` 填这两个字段。空 DTO 比较在只设 `redirect_stderr: true` 时必须产出 proto（现有 `s.Log != (LogPolicyDTO{})` 已满足）。

`localhttp` DTO 与 `specToDTO` / `dtoToSpec` 同样加字段。

`cli/process.go` `processGet` 在打印 instances 之后：

```go
for _, inst := range p.GetInstances() {
	if inst.GetLogPathPending() {
		fmt.Fprintln(stdout, "log path pending: restart to apply")
		break
	}
}
```

- [ ] **Step 4: API 测试**

在 `internal/api/convert_test.go` 断言 `SpecToProto` 往返 `Directory` / `RedirectStderr`。

在已有 process API 测试里 Apply 一个带 `directory` 的 spec，Get 回来字段还在。若现有 `log_test.go` 写死 `data-dir/logs/...`，改为启动后读自定义目录。

- [ ] **Step 5: 跑测试**

Run: `go test ./internal/api ./internal/cli ./internal/localhttp -count=1`

Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add proto/procmesh/v1 web/src/gen/procmesh/v1 internal/api internal/cli/specfile.go internal/cli/process.go internal/localhttp internal/process/manager.go
git commit -m "$(cat <<'EOF'
feat(api): expose log directory, redirect_stderr, and pending flag

EOF
)"
```

---

### Task 5: 同分区自定义目录参与自动删除

**Files:**
- Modify: `internal/logmgr/logmgr.go`
- Modify: `internal/logmgr/logmgr_test.go`
- Modify: `internal/agent/run.go`

**Interfaces:**
- Produces: `logmgr.Manager.ExtraLogDirs []string`
- Produces: `logmgr.Manager.SameDeviceFn func(a, b string) bool`（测试可注入；nil 则用 `SameDevice`）

- [ ] **Step 1: 写失败测试**

```go
func TestProtect_DeletesSameDeviceCustomDirOnlyManagedFiles(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(t.TempDir(), "app")
	managed := filepath.Join(custom, "0", "stdout.log")
	other := filepath.Join(custom, "notes.txt")
	if err := os.MkdirAll(filepath.Dir(managed), 0o750); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.WriteFile(managed, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(managed, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	pol := logmgr.DefaultPolicy()
	pol.AutoDelete = true
	m := &logmgr.Manager{
		Root:         root,
		Usage:        func(string) (float64, error) { return 96, nil },
		Now:          time.Now,
		Policy:       pol,
		ExtraLogDirs: []string{custom},
		SameDeviceFn: func(a, b string) bool { return true },
	}
	if _, err := m.Protect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatal("managed custom log should be deleted")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("unrelated file must stay")
	}
}

func TestProtect_SkipsOtherDeviceCustomDir(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(t.TempDir(), "app")
	managed := filepath.Join(custom, "0", "stdout.log")
	if err := os.MkdirAll(filepath.Dir(managed), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	pol := logmgr.DefaultPolicy()
	pol.AutoDelete = true
	m := &logmgr.Manager{
		Root:         root,
		Usage:        func(string) (float64, error) { return 96, nil },
		Policy:       pol,
		ExtraLogDirs: []string{custom},
		SameDeviceFn: func(a, b string) bool { return false },
	}
	if _, err := m.Protect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(managed); err != nil {
		t.Fatal("other-device custom log must stay")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/logmgr -run 'TestProtect_DeletesSameDeviceCustomDirOnlyManagedFiles|TestProtect_SkipsOtherDeviceCustomDir' -count=1`

Expected: FAIL。

- [ ] **Step 3: 实现**

`Manager` 增加：

```go
ExtraLogDirs []string
SameDeviceFn func(a, b string) bool
```

`listDeletableLogs` 改为接收 `m`，先走现有 `root/logs`，再对每个 `ExtraLogDirs`：

```go
fn := m.SameDeviceFn
if fn == nil {
	fn = SameDevice
}
if !fn(m.Root, extra) {
	continue
}
```

只枚举 `extra` 下**名字为十进制整数**的子目录，其中文件名满足现有 `deletableLogName` 的才加入列表。`notes.txt`、`extra/stdout.log` 不删。

去重：用 `map[string]struct{}` 按 path 去重后再按 mtime 排序。

`internal/agent/run.go` 在 `logs.Protect` 前：

```go
if logs != nil {
	logs.ExtraLogDirs = mgr.CustomLogDirs(ctx)
	if _, err := logs.Protect(ctx); err != nil {
		opt.Logger.Warn("disk protection failed", "error", err)
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/logmgr ./internal/agent -count=1`

Expected: PASS（agent 包若太慢，至少 `./internal/logmgr`）。

- [ ] **Step 5: Commit**

```bash
git add internal/logmgr/logmgr.go internal/logmgr/logmgr_test.go internal/agent/run.go
git commit -m "$(cat <<'EOF'
feat(logmgr): auto-delete same-device custom process log dirs

EOF
)"
```

---

### Task 6: Web 配置、pending 横幅、日志提示

**Files:**
- Modify: `web/src/pages/processConfigForm.ts`
- Modify: `web/src/pages/processConfigSchema.ts`
- Modify: `web/src/pages/processConfigForm.test.ts`
- Modify: `web/src/pages/ProcessConfigForm.component.test.ts`（默认 fixture）
- Modify: `web/src/pages/processView.ts`
- Modify: `web/src/pages/ProcessDetailPage.vue`
- Modify: `web/src/pages/ProcessConfigPanel.vue`
- Modify: `web/src/pages/ProcessLogsPanel.vue`
- Modify: `web/src/pages/ProcessLogsPanel.test.ts`
- Modify: `web/public/locales/en/common.json`
- Modify: `web/public/locales/zh/common.json`

- [ ] **Step 1: 表单字段与校验测试**

`processConfigForm.ts`：

```ts
log: {
  directory: string;
  redirectStderr: boolean;
  maxSize: string;
  maxFiles: string;
  maxAgeSeconds: string;
  compress: boolean;
};
```

`ProcessConfigIssueCode` 增加 `"invalidLogDirectory"`。

`shouldIncludeLog` 加上 `form.log.directory.trim() !== "" || form.log.redirectStderr`。

`processConfigFormToSpec` 的 `LogPolicy` 加上 `directory`、`redirectStderr`。

`specToProcessConfigForm` 读取这两项。

校验：`directory` 非空且 `!form.log.directory.trim().startsWith("/")` → `invalidLogDirectory`。完整危险路径仍由服务端拒绝并展示 `INVALID: log path: ...`。

`processConfigSchema.ts` 在 `log.maxSize` 前插入：

```ts
{ path: "log.directory", section: "logsResources", control: "text", labelKey: "processConfig.editor.field.logDirectory" },
{ path: "log.redirectStderr", section: "logsResources", control: "boolean", labelKey: "processConfig.editor.field.redirectStderr" },
```

同步 `ProcessConfigFieldPath` union。

`processConfigForm.test.ts` 的路径列表与默认 fixture 补上新字段。相对路径应得到 `invalidLogDirectory`。

- [ ] **Step 2: i18n**

`en/common.json`：

```json
"logDirectory": "Log directory",
"redirectStderr": "Redirect stderr to stdout",
```

validation：

```json
"invalidLogDirectory": "Log directory must be an absolute path"
```

`processDetail` / `processConfig`：

```json
"logPathPending": "Log path will apply after restart"
```

`processLogs`：

```json
"stderrMerged": "stderr is merged into stdout",
"stderrMergePending": "stderr will merge into stdout after restart"
```

`zh/common.json` 对应：

- `日志目录`
- `将 stderr 重定向到 stdout`
- `日志目录必须是绝对路径`
- `日志路径将在重启后生效`
- `stderr 已合并到 stdout`
- `重启后 stderr 将合并到 stdout`

然后：`cd web && npm run i18n:types && npm run i18n:check`

- [ ] **Step 3: pending 横幅与日志提示**

`processView.ts` 的 `ProcessDetailView` 增加：

```ts
logPathPending: boolean;
redirectStderr: boolean;
```

`mapProcessDetail`：

```ts
const log = pick(spec, "log") ?? {};
logPathPending: instances.some((inst) => toBool(pick(asRecord(inst), "logPathPending", "log_path_pending"))),
redirectStderr: toBool(pick(asRecord(log), "redirectStderr", "redirect_stderr")),
```

`ProcessDetailPage.vue` 在现有 restart banner 旁：

```vue
<div v-if="detail.logPathPending" class="banner" role="status">
  {{ t("processDetail.logPathPending") }}
</div>
```

把 `logPathPending` / `redirectStderr` 传给 `ProcessLogsPanel`。

`ProcessLogsPanel` 增加 props：`redirectStderr?: boolean`、`logPathPending?: boolean`。stderr 页签保持可点。在控件下：

```vue
<p v-if="streamName === 'stderr' && redirectStderr && !logPathPending" class="muted" role="status">
  {{ t("processLogs.stderrMerged") }}
</p>
<p v-else-if="streamName === 'stderr' && redirectStderr && logPathPending" class="muted" role="status">
  {{ t("processLogs.stderrMergePending") }}
</p>
```

注意：pending 且最新 Spec 已打开 redirect 时，effective 可能尚未重定向，stderr 仍有内容。此时显示 `stderrMergePending`。`redirectStderr` 取**最新 Spec**。

`ProcessConfigPanel.vue`：增加 `useProcessClient().getProcess`（queryKey 已有 `processKey`），任一 `instance.logPathPending` 为 true 时在表单顶部显示 `t("processConfig.logPathPending")`。保存失败时 `actionError` 已是 `formatRemoteError`，会带上 `INVALID: log path: ...`。

- [ ] **Step 4: 跑前端测试与检查**

Run:

```bash
cd web && npm test -- --run src/pages/processConfigForm.test.ts src/pages/ProcessLogsPanel.test.ts
cd web && npm run i18n:check
```

Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add web/src/pages web/public/locales web/src/types/i18n.d.ts
git commit -m "$(cat <<'EOF'
feat(web): configure process log directory and show pending path

EOF
)"
```

---

### Task 7: 端到端核对

- [ ] **Step 1: 全量 Go 测试**

Run: `go test ./internal/logmgr ./internal/process ./internal/api ./internal/cli ./internal/localhttp ./internal/agent -count=1`

Expected: PASS。

- [ ] **Step 2: 对照 spec §9**

1. 未配 directory → 默认路径（Task 1 / 现有 reconcile 测试）。
2. 自定义目录 + ordinal（Task 1 / 3）。
3. redirect 只写 stdout，stderr tail 为空（Task 3 + API ReadLogPath）。
4. 改目录未重启：轮转旧文件、pending=true（Task 3 + 4）。
5. 非法路径文案含 `log path:`（Task 1 / 2）。
6. 同分区删托管文件、异分区不删、无关文件不删（Task 5）。
7. 紧急停写仍 `/dev/null`（现有行为，write 路径在 Protect 之后）。

- [ ] **Step 3: 若改了 Web UI，按用户规则在浏览器走一遍**

配置页填目录、勾选 redirect、保存；详情页看到 pending 横幅；日志页切 stderr 看到提示且不自动重启。桌面与窄屏各看一次。

- [ ] **Step 4: 收尾 commit（仅当还有未提交修正）**

```bash
git add -A
git status
# 有实质改动再 commit
git commit -m "$(cat <<'EOF'
test: cover process log directory acceptance cases

EOF
)"
```

---

## Spec coverage

| Spec | Task |
|------|------|
| §3 directory / redirect_stderr 字段 | 2, 4, 6 |
| §4 默认 vs 自定义路径 | 1, 3 |
| §4.1 effective spec | 3, 4 |
| §5 / §5.1 校验与 `log path:` 文案 | 1, 2 |
| §6.1 Start / Prepare / /dev/null | 3 |
| §6.2 读 stderr 不改读 stdout | 3, 4, 6 |
| §6.3 轮转 effective 路径 | 3 |
| §6.4 不搬家 | 3 |
| §6.5 log_path_pending | 3, 4, 6 |
| §7 同分区自动删除 | 5 |
| §8 CLI / Web / i18n | 4, 6 |
| §9 测试清单 | 7 |
