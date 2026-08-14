# 磁盘保护可配置 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `procmesh-agent` 从 `agent.yaml` 读取磁盘保护三档百分比与两个独立开关；默认 85/90/95、不自动删日志、仍开启紧急停写。

**Architecture:** `internal/agentcfg` 只负责找文件、解析 YAML、填默认、校验。`logmgr.Policy` 驱动 `Protect` / `classify` / `WritesAllowed`，不再把 85/90/95 当作不可改常量。Agent 增加 `--config`；未指定时按 OS 默认路径，文件缺失则用内置默认。

**Tech Stack:** 已有 Go 1.23、`gopkg.in/yaml.v3`。

**Spec:** `docs/superpowers/specs/2026-08-14-disk-protect-config-design.md`

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- `logmgr` 不得解析 YAML、不得 import `agentcfg`
- 默认：`WarnPercent=85`、`CleanupPercent=90`、`EmergencyPercent=95`、`AutoDelete=false`、`EmergencyStopWrites=true`
- 占用仍用 Statfs 百分比，`used > N` 触发该档；不按剩余字节判定
- 校验：三个百分比整数 `1..100`，且 `warn < cleanup < emergency`
- `--config` 指向的文件必须存在且合法，否则启动失败
- 未传 `--config`：Linux `/etc/procmesh/agent.yaml`，macOS `$HOME/.config/procmesh/agent.yaml`；默认路径不存在则静默用默认
- 未知 YAML 顶层键忽略；只解析 `disk:`
- `auto_delete` 与 `emergency_stop_writes` 互不影响
- 进程 `log` 轮转（RotateLogs）不受这两个开关影响
- 进入紧急停写后，占用回到 `cleanup_percent`（含）及以下才恢复写入
- 测试与代码同目录；强制 TDD
- 文档与提交说明使用中文

## File map

```text
internal/logmgr/policy.go          # Policy、DefaultPolicy、Validate
internal/logmgr/logmgr.go          # Protect/classify 改用 Policy
internal/logmgr/logmgr_test.go
internal/agentcfg/load.go
internal/agentcfg/load_test.go
internal/agent/run.go
cmd/procmesh-agent/main.go
docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md
```

---

### Task 1: `logmgr.Policy` 驱动 Protect

**Files:**
- Create: `internal/logmgr/policy.go`
- Modify: `internal/logmgr/logmgr.go`
- Modify: `internal/logmgr/logmgr_test.go`

**Interfaces:**
- Consumes: 现有 `Protect`、`WritesAllowed`、`Usage`
- Produces:

```go
type Policy struct {
    WarnPercent          int
    CleanupPercent       int
    EmergencyPercent     int
    AutoDelete           bool
    EmergencyStopWrites  bool
}

func DefaultPolicy() Policy // 85, 90, 95, AutoDelete=false, EmergencyStopWrites=true

func (p Policy) Validate() error
// 百分比不在 1..100，或 !(warn < cleanup < emergency) → errcode.INVALID

type Manager struct {
    Root   string
    Usage  DiskUsage
    Now    func() time.Time
    Policy Policy // 零值在 Protect 前视为 DefaultPolicy()
}

func (m *Manager) policy() Policy // Policy 全零则 DefaultPolicy()
```

`Protect` 规则（一字不差）：

1. `level := classify(p, used)`：`used > EmergencyPercent` → Emergency；`used > CleanupPercent` → Cleanup；`used > WarnPercent` → Warn；否则 OK。
2. **仅当** `p.AutoDelete && level >= Cleanup` 时调用 `deleteOldestLogs`，停止线为 `float64(p.WarnPercent)`（替代写死的 85）。
3. **仅当** `p.EmergencyStopWrites && level == Emergency` 时置 `blocked=true`。
4. `blocked && used <= float64(p.CleanupPercent)` 时清 `blocked`。

`classify` 改为 `func classify(p Policy, used float64) Level`。包级 `warnPct` 等可保留为 DefaultPolicy 的字面来源，或删掉改用 Policy 字段。

现有依赖「91% 会删日志」的测试必须显式 `Policy: logmgr.DefaultPolicy()` 后设 `AutoDelete: true`（默认已改为不删）。Emergency 相关测试保持默认 `EmergencyStopWrites: true` 即可。

- [ ] **Step 1: 写失败测试（追加到 `logmgr_test.go`）**

```go
func TestDefaultPolicy_DoesNotAutoDelete(t *testing.T) {
	p := logmgr.DefaultPolicy()
	if p.WarnPercent != 85 || p.CleanupPercent != 90 || p.EmergencyPercent != 95 {
		t.Fatalf("%+v", p)
	}
	if p.AutoDelete || !p.EmergencyStopWrites {
		t.Fatalf("%+v", p)
	}
}

func TestProtect_AutoDeleteFalseKeepsLogsAt91(t *testing.T) {
	root := t.TempDir()
	logPath := writeLog(t, root, "p", "i", "stdout.log", "keep", time.Now())
	m := &logmgr.Manager{
		Root:   root,
		Usage:  func(string) (float64, error) { return 91, nil },
		Policy: logmgr.DefaultPolicy(), // AutoDelete=false
		Now:    time.Now,
	}
	lvl, err := m.Protect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lvl != logmgr.Cleanup {
		t.Fatalf("lvl=%v", lvl)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatal("auto_delete false must keep logs")
	}
}

func TestProtect_EmergencyStopWritesFalseAllowsWritesAt96(t *testing.T) {
	root := t.TempDir()
	p := logmgr.DefaultPolicy()
	p.EmergencyStopWrites = false
	m := &logmgr.Manager{
		Root:   root,
		Usage:  func(string) (float64, error) { return 96, nil },
		Policy: p,
		Now:    time.Now,
	}
	lvl, err := m.Protect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lvl != logmgr.Emergency {
		t.Fatalf("lvl=%v", lvl)
	}
	if !m.WritesAllowed() {
		t.Fatal("emergency_stop_writes false must allow writes")
	}
}

func TestPolicy_ValidateOrder(t *testing.T) {
	p := logmgr.DefaultPolicy()
	p.WarnPercent = 90
	p.CleanupPercent = 85
	if err := p.Validate(); err == nil {
		t.Fatal("expected invalid order")
	}
}
```

同时改现有 `TestProtect_CleanupDeletesOldestUntil85` 与 `TestProtect_DeletesRotatedArchives`：给 `Manager` 设 `Policy` 且 `AutoDelete: true`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/logmgr/ -count=1 -run 'TestDefaultPolicy_|TestProtect_AutoDelete|TestProtect_EmergencyStopWritesFalse|TestPolicy_Validate'`
Expected: FAIL（`Policy` / `DefaultPolicy` 未定义）。

- [ ] **Step 3: 实现 Policy 并改 Protect**

`internal/logmgr/policy.go`：

```go
package logmgr

import "github.com/qleelulu/procmesh/internal/errcode"

type Policy struct {
	WarnPercent         int
	CleanupPercent      int
	EmergencyPercent    int
	AutoDelete          bool
	EmergencyStopWrites bool
}

func DefaultPolicy() Policy {
	return Policy{
		WarnPercent:         85,
		CleanupPercent:      90,
		EmergencyPercent:    95,
		AutoDelete:          false,
		EmergencyStopWrites: true,
	}
}

func (p Policy) Validate() error {
	if p.WarnPercent < 1 || p.WarnPercent > 100 ||
		p.CleanupPercent < 1 || p.CleanupPercent > 100 ||
		p.EmergencyPercent < 1 || p.EmergencyPercent > 100 {
		return errcode.E(errcode.INVALID, "disk percent out of range")
	}
	if !(p.WarnPercent < p.CleanupPercent && p.CleanupPercent < p.EmergencyPercent) {
		return errcode.E(errcode.INVALID, "disk percents must be warn < cleanup < emergency")
	}
	return nil
}
```

`Manager.policy()`：若 `WarnPercent==0 && CleanupPercent==0 && EmergencyPercent==0` 则 `DefaultPolicy()`（避免与「显式全 0」混淆；Validate 会拒绝 0）。

按上面四条改 `Protect` / `classify`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/logmgr/ ./internal/process/ ./internal/agent/ -count=1`
Expected: PASS。给现有 Cleanup 测试补上 `AutoDelete: true` 后它们必须仍然删文件。

- [ ] **Step 5: Commit**

```bash
git add internal/logmgr
git commit -m "feat: 磁盘保护使用可配置 Policy，默认不自动删日志"
```

---

### Task 2: 加载 `agent.yaml`

**Files:**
- Create: `internal/agentcfg/load.go`
- Create: `internal/agentcfg/load_test.go`

**Interfaces:**
- Consumes: `logmgr.Policy`、`logmgr.DefaultPolicy`、`logmgr.Policy.Validate`
- Produces:

```go
func DefaultPath() string
// darwin: filepath.Join(home, ".config/procmesh/agent.yaml")
// 其他（含 linux）: /etc/procmesh/agent.yaml
// home 用 os.UserHomeDir()；失败时仍返回 ~/.config/... 的字面不可用路径，Load("", false) 会当缺失

func Load(path string, required bool) (logmgr.Policy, error)
// path=="" && !required → DefaultPolicy()
// required && (path=="" || 文件不存在) → errcode.INVALID
// !required && 文件不存在 → DefaultPolicy()
// 文件存在但 YAML 非法 / Validate 失败 → 返回错误（即使 !required）
```

YAML 结构：

```go
type file struct {
    Disk *diskFile `yaml:"disk"`
}
type diskFile struct {
    WarnPercent         *int  `yaml:"warn_percent"`
    CleanupPercent      *int  `yaml:"cleanup_percent"`
    EmergencyPercent    *int  `yaml:"emergency_percent"`
    AutoDelete          *bool `yaml:"auto_delete"`
    EmergencyStopWrites *bool `yaml:"emergency_stop_writes"`
}
```

指针字段：nil 表示省略，保留 DefaultPolicy 对应值。未知键由 yaml.v3 默认忽略。

- [ ] **Step 1: 写失败测试**

```go
func TestLoad_MissingOptionalUsesDefaults(t *testing.T) {
	p, err := agentcfg.Load(filepath.Join(t.TempDir(), "nope.yaml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if p != logmgr.DefaultPolicy() {
		t.Fatalf("%+v", p)
	}
}

func TestLoad_MissingRequiredErrors(t *testing.T) {
	_, err := agentcfg.Load(filepath.Join(t.TempDir(), "nope.yaml"), true)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoad_PartialDiskKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte("disk:\n  auto_delete: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := agentcfg.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !p.AutoDelete || p.WarnPercent != 85 || !p.EmergencyStopWrites {
		t.Fatalf("%+v", p)
	}
}

func TestLoad_InvalidOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	body := "disk:\n  warn_percent: 90\n  cleanup_percent: 80\n  emergency_percent: 95\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Load(path, true); err == nil {
		t.Fatal("expected validate error")
	}
}

func TestDefaultPath_DarwinOrLinux(t *testing.T) {
	p := agentcfg.DefaultPath()
	if runtime.GOOS == "darwin" {
		if !strings.HasSuffix(p, "/.config/procmesh/agent.yaml") {
			t.Fatalf("%s", p)
		}
	} else if p != "/etc/procmesh/agent.yaml" {
		t.Fatalf("%s", p)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agentcfg/ -count=1`
Expected: FAIL（包不存在）。

- [ ] **Step 3: 实现 `load.go`**

用 `os.ReadFile` + `yaml.Unmarshal`。`required==true` 时 `os.IsNotExist` → `errcode.E(errcode.INVALID, "config file not found")`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/agentcfg/ ./internal/logmgr/ -count=1`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/agentcfg
git commit -m "feat: 从 agent.yaml 加载磁盘保护配置"
```

---

### Task 3: Agent 接入 `--config`

**Files:**
- Modify: `cmd/procmesh-agent/main.go`
- Modify: `internal/agent/run.go`
- Modify: `docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md`（§12 磁盘保护补一句：阈值可配，默认不自动删、默认紧急停写）

**Interfaces:**
- Consumes: `agentcfg.Load`、`agentcfg.DefaultPath`、`logmgr.Policy`
- Produces:

```go
// agent.Options 增加：
ConfigPath string

// Run 开头（在创建 logmgr.Manager 之前）：
//   path := opt.ConfigPath
//   required := path != ""
//   if path == "" { path = agentcfg.DefaultPath() }
//   pol, err := agentcfg.Load(path, required)
//   if err != nil { return err }
//   logs := &logmgr.Manager{Root: layout.Root, Now: time.Now, Policy: pol}
```

`main.go`：

```go
config := flag.String("config", "", "agent.yaml path (optional)")
// ...
ConfigPath: *config,
```

- [ ] **Step 1: 写失败测试（`internal/agent/config_test.go`）**

```go
func TestRun_RejectsMissingExplicitConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := agent.Run(ctx, agent.Options{
		DataDir:    t.TempDir(),
		Listen:     "127.0.0.1:0",
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
	})
	if err == nil {
		t.Fatal("expected missing config error")
	}
}

func TestRun_AppliesAutoDeleteFromConfig(t *testing.T) {
	// 不必起完整 HTTP：若 Run 在 Listen 前就 Load，缺失文件测试已够。
	// 本测试写合法 yaml（auto_delete: true）并 Listen :0，OnListen 后 cancel。
	// 只断言 Run 返回 nil（ctx 取消），配置非法才会非 nil。
	dir := t.TempDir()
	cfg := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(cfg, []byte("disk:\n  auto_delete: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx, agent.Options{
			DataDir:    dir,
			Listen:     "127.0.0.1:0",
			ConfigPath: cfg,
			OnListen:   func(string) { cancel() },
		})
	}()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timeout")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/ -count=1 -run 'TestRun_RejectsMissing|TestRun_AppliesAutoDelete'`
Expected: FAIL（`ConfigPath` 未定义或被忽略）。

- [ ] **Step 3: 接线并改 spec §12**

在架构 spec「磁盘保护」列表后追加：

> V1.0 阈值与开关见 `agent.yaml` 的 `disk`（`docs/superpowers/specs/2026-08-14-disk-protect-config-design.md`）。默认 85/90/95、`auto_delete: false`、`emergency_stop_writes: true`。

- [ ] **Step 4: 跑测试确认通过**

Run:

```bash
go test ./internal/agent/ ./internal/agentcfg/ ./internal/logmgr/ -count=1
go test ./... -count=1
```

Expected: 全 PASS。

- [ ] **Step 5: Commit**

```bash
git add cmd/procmesh-agent internal/agent docs/superpowers/specs/2026-08-13-v1-mvp-architecture-design.md
git commit -m "feat: Agent 通过 --config 加载磁盘保护配置"
```

---

## Self-review（计划 vs spec）

| Spec 项 | 任务 |
|---------|------|
| `--config` / 默认路径 / 缺失语义 | 2–3 |
| 五字段与默认值 | 1–2 |
| warn < cleanup < emergency、1..100 | 1–2 |
| `auto_delete` 默认 false，91% 不删 | 1 |
| `emergency_stop_writes` 默认 true；false 时 96% 仍可写 | 1 |
| 两开关独立 | 1 |
| 轮转不受影响 | 1（不改 RotateLogs） |
| 不把 data-dir/listen 迁入 YAML | 全计划省略 |
| 更新架构 spec | 3 |
