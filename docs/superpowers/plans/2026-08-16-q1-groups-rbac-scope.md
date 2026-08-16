# Q1 Groups + RBAC Scope Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** 已完成（2026-08-17）。14 个 TDD 任务均已落地；`internal/web/dist` 已按 Q1 Web 源码重建并提交。不要开始 Q2，直到本阶段合并。

**Goal:** 让 Agent Group 成为 Raft 一等公民，Process Group 仍是 Owner `spec.group` 字符串，RBAC 增加 `AGENT_GROUP` / `PROCESS_GROUP`，使 Finance Operator 不能操作范围外的节点和进程。

**Architecture:** 组成员与组对象只写 Raft FSM。`ProcessSpec.Group` 继续走 `ApplySpec` + CAS。Gossip `ProcessSummary` 增加 `process_id` 与 `group`（摘要，不是权威）。`State.Check` 保留三参数包装，新增 `CheckTarget` 做组求值。List API 先 `CanAny(perm)` 再按条 `CheckTarget` 过滤。不实现 Batch / Alert / Backup。

**Tech Stack:** 现有 Go + hashicorp/raft + ConnectRPC + Vue3 + `make proto` / `make proto-ts`。

---

## Global Constraints

- 模块路径：`github.com/qleelulu/procmesh`
- 强制 TDD：先红后绿；每任务先写失败测试
- `process` 不得 import `cluster` 或 `control`
- Raft 不存 process group 注册表、不存 batch/alert/metrics
- Gossip 不加 mutation / 组成员表（组成员以 Raft 为准）
- `version.Protocol` 保持 `1`
- 错误码：`INVALID` / `CONFLICT` / `DENIED` / `NOT_FOUND` / `UNAVAILABLE`
- 新 perm 写入内置角色；自定义 role 不改
- Mutation 必须带 `operation_id`
- 测试与代码同目录；覆盖率：`internal/control`、`internal/auth`、`internal/process` ≥ 80%
- 文档与计划用中文；API 错误消息用英文
- 本阶段不做：BatchService、AlertService、BackupService、历史指标

## 规格解读（Q1 边界）

来源：`docs/superpowers/specs/2026-08-16-v1.1-architecture-design.md` §5、§10、§12 Q1、§13.7–§13.8。

1. Agent Group：`group_id` UUID、`name` 集群唯一、`[A-Za-z0-9._-]{1,64}`、显式 `member_node_ids`、一节点可多组。
2. 删组：仍有 `AGENT_GROUP` binding → `CONFLICT`。
3. `CmdMemberRemove` 必须从所有组摘掉该 `node_id`。
4. Process Group：空 = 未分组；非空 trim 后同样字符集 1–64；改组 = 改 spec + CAS。
5. RBAC 并集，不做否定/交集。
6. Grant `AGENT_GROUP` 时组必须已存在；Grant `PROCESS_GROUP` 只校验名字，组不必已有进程。
7. 可演示：Finance Operator 不能碰其它组节点/进程。

## File map

```text
internal/process/validate.go
internal/process/validate_test.go
internal/cluster/summary.go
internal/cluster/codec_test.go
internal/agent/summary.go
internal/agent/summary_test.go
internal/control/command.go
internal/control/fsm.go
internal/control/fsm_test.go
internal/auth/perm.go
internal/auth/rbac.go
internal/auth/rbac_test.go
internal/api/rbac.go
internal/api/role.go
internal/api/role_test.go
internal/api/group.go                 # 新建
internal/api/group_test.go            # 新建
internal/api/node.go
internal/api/process.go
internal/api/process_test.go
internal/api/server.go
proto/procmesh/v1/api.proto
internal/cli/root.go
internal/cli/client.go
internal/cli/group.go                 # 新建
internal/cli/user.go
web/src/lib/rpc.ts
web/src/router.ts
web/src/components/AppShell.vue
web/src/pages/GroupsPage.vue          # 新建
web/src/pages/GroupsPage.test.ts      # 新建
web/src/pages/RolesPage.vue
web/src/pages/ProcessesPage.vue
web/public/locales/en/common.json
web/public/locales/zh/common.json
internal/agent/q1_accept_test.go      # 新建
```

生成（改 proto 后执行，不要手改）：`proto/procmesh/v1/api.pb.go`、`proto/procmesh/v1/procmeshv1connect/api.connect.go`、`web/src/gen/procmesh/v1/api_pb.ts`

---

### Task 1: Process group 名字校验

**Files:**
- Modify: `internal/process/validate.go`
- Test: `internal/process/validate_test.go`

- [x] **Step 1: Write the failing test**

在 `internal/process/validate_test.go` 追加：

```go
func TestValidateSpec_RejectsInvalidGroup(t *testing.T) {
	s := validSpec()
	s.Group = "bad group"
	if err := process.ValidateSpec(s); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSpec_AcceptsEmptyAndValidGroup(t *testing.T) {
	s := validSpec()
	if err := process.ValidateSpec(s); err != nil {
		t.Fatal(err)
	}
	s.Group = "finance"
	if err := process.ValidateSpec(s); err != nil {
		t.Fatal(err)
	}
	s.Group = "  finance  "
	if err := process.ValidateSpec(s); err != nil {
		t.Fatal(err)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/process -run 'TestValidateSpec_RejectsInvalidGroup|TestValidateSpec_AcceptsEmptyAndValidGroup' -count=1`

Expected: `RejectsInvalidGroup` FAIL（非法 group 被接受）

- [x] **Step 3: Write minimal implementation**

在 `internal/process/validate.go`：

```go
var groupRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func ValidateSpec(s ProcessSpec) error {
	// existing name/command/instances/deps/restart checks...
	g := strings.TrimSpace(s.Group)
	if g != "" && !groupRE.MatchString(g) {
		return errcode.E(errcode.INVALID, "group")
	}
	return nil
}
```

加 `"strings"` import。不要在 `ValidateSpec` 里改 `s.Group`（值类型，调用方应自己 trim；`ApplySpec` 路径若拷贝 spec，在 Manager 写入前 `spec.Group = strings.TrimSpace(spec.Group)`——若现有 `ApplySpec` 直接存用户值，本任务只校验 trim 后的副本：在 ValidateSpec 开头 `s.Group = strings.TrimSpace(s.Group)` 即可，因为 `s` 是值传递，只影响本函数校验）。

更好：校验用 trim 后的值，不写回。空或合法通过。

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/process -count=1`

Expected: PASS；包覆盖率仍 ≥ 80%

- [x] **Step 5: Commit**

```bash
git add internal/process/validate.go internal/process/validate_test.go
git commit -m "feat(process): validate ProcessSpec.Group name"
```

---

### Task 2: Gossip ProcessSummary 增加 process_id 与 group

**Files:**
- Modify: `internal/cluster/summary.go`
- Modify: `internal/cluster/codec_test.go`
- Modify: `internal/agent/summary.go`
- Modify: `internal/api/node.go` (`nodeToProto`)

- [x] **Step 1: Write the failing test**

在 `internal/cluster/codec_test.go` 的 `TestEncodeState_KeepsProcessSummary` 改为断言新字段，并追加：

```go
func TestDecodeState_IgnoresUnknownProcessFields(t *testing.T) {
	raw := []byte(`{"node_id":"n1","processes":[{"name":"web","process_id":"p1","group":"finance","extra":1}]}`)
	got, err := cluster.DecodeState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Processes[0].ProcessID != "p1" || got.Processes[0].Group != "finance" {
		t.Fatalf("%+v", got.Processes[0])
	}
}
```

先改 `TestEncodeState_KeepsProcessSummary`：

```go
Processes: []cluster.ProcessSummary{{
    ProcessID: "pid-1", Name: "web", Group: "finance",
    Desired: "RUNNING", Observed: "RUNNING",
    Health: "HEALTHY", LatestRevision: 3, ActiveRevision: 3,
    FreshnessUnixMs: 100,
}},
// ...
if p := got.Processes[0]; p.ProcessID != "pid-1" || p.Group != "finance" || p.Name != "web" {
    t.Fatalf("%+v", p)
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cluster -run 'TestEncodeState_KeepsProcessSummary|TestDecodeState_IgnoresUnknownProcessFields' -count=1`

Expected: FAIL（`ProcessID` / `Group` 字段不存在）

- [x] **Step 3: Write minimal implementation**

`internal/cluster/summary.go`：

```go
type ProcessSummary struct {
	ProcessID       string `json:"process_id,omitempty"`
	Name            string `json:"name"`
	Group           string `json:"group,omitempty"`
	Desired         string `json:"desired"`
	Observed        string `json:"observed"`
	Health          string `json:"health"`
	LatestRevision  int64  `json:"latest_revision"`
	ActiveRevision  int64  `json:"active_revision"`
	FreshnessUnixMs int64  `json:"freshness_unix_ms"`
}
```

`internal/agent/summary.go` `processSummaries`：

```go
sum := cluster.ProcessSummary{
	ProcessID:       spec.ProcessID,
	Name:            spec.Name,
	Group:           spec.Group,
	LatestRevision:  spec.LatestRevision,
	FreshnessUnixMs: now,
}
```

`internal/api/node.go` `nodeToProto` 映射 `ProcessId` / `Group`（proto 字段在 Task 8 才生成；**本任务先只改 cluster JSON 与 agent summary**。`nodeToProto` 放到 Task 8 之后，避免编译失败）。

本任务不要改 proto。

若 `internal/agent/summary_test.go` 有 Snapshot 断言，同步补字段（没有则跳过）。

- [x] **Step 4: Run tests**

Run: `go test ./internal/cluster ./internal/agent -count=1`

Expected: PASS（agent 包若只是多填字段，旧测试仍过）

- [x] **Step 5: Commit**

```bash
git add internal/cluster/summary.go internal/cluster/codec_test.go internal/agent/summary.go
git commit -m "feat(cluster): gossip process_id and group on ProcessSummary"
```

---

### Task 3: AgentGroup FSM 命令

**Files:**
- Modify: `internal/control/command.go`
- Modify: `internal/control/fsm.go`
- Test: `internal/control/fsm_test.go`

- [x] **Step 1: Write the failing test**

在 `internal/control/fsm_test.go` 追加（沿用已有 `mustBootstrap` / `mustEncode`）：

```go
func TestFSM_AgentGroupCRUD(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	if err := s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(mustEncode(t, "group_put", control.GroupPutBody{
		GroupID: "g-fin", Name: "finance", Description: "fin", NowUnix: now.Unix(),
	}), now); err != nil {
		t.Fatal(err)
	}
	g, ok := s.AgentGroups["g-fin"]
	if !ok || g.Name != "finance" {
		t.Fatalf("group %+v ok=%v", g, ok)
	}
	if err := s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	if !s.NodeInGroup("node-a", "g-fin") {
		t.Fatal("expected member")
	}
	if err := s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "missing"}), now); err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("missing node: %v", err)
	}
	if err := s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "g2", Name: "finance", NowUnix: now.Unix()}), now); err == nil || !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("dup name: %v", err)
	}
	if err := s.Apply(mustEncode(t, "group_delete", control.GroupDeleteBody{GroupID: "g-fin"}), now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AgentGroups["g-fin"]; ok {
		t.Fatal("group should be gone")
	}
}

func TestFSM_GroupNameValidation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	err := s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "g1", Name: "bad name", NowUnix: now.Unix()}), now)
	if err == nil || !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/control -run 'TestFSM_AgentGroupCRUD|TestFSM_GroupNameValidation' -count=1`

Expected: FAIL（unknown command type 或缺类型）

- [x] **Step 3: Write minimal implementation**

`internal/control/command.go` 常量：

```go
CmdGroupPut        = "group_put"
CmdGroupDelete     = "group_delete"
CmdGroupMemberAdd  = "group_member_add"
CmdGroupMemberRemove = "group_member_remove"
```

类型：

```go
type AgentGroup struct {
	GroupID     string   `json:"group_id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	MemberIDs   []string `json:"member_node_ids,omitempty"`
	CreatedUnix int64    `json:"created_unix"`
	UpdatedUnix int64    `json:"updated_unix"`
}

type GroupPutBody struct {
	GroupID, Name, Description string
	NowUnix                    int64
}

type GroupDeleteBody struct {
	GroupID string `json:"group_id"`
}

type GroupMemberBody struct {
	GroupID, NodeID string
}
```

`fsm.go`：

```go
const (
	ScopeCluster      ScopeType = "CLUSTER"
	ScopeAgent        ScopeType = "AGENT"
	ScopeAgentGroup   ScopeType = "AGENT_GROUP"
	ScopeProcessGroup ScopeType = "PROCESS_GROUP"
)

// State 增加：
AgentGroups map[string]AgentGroup `json:"agent_groups"`

func (s *State) ensure() {
	// existing...
	if s.AgentGroups == nil {
		s.AgentGroups = map[string]AgentGroup{}
	}
}

var agentGroupNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func (s *State) Apply(...) {
	// switch 增加四个 case，调用 applyJSON
}

func (s *State) applyGroupPut(b GroupPutBody) error {
	name := strings.TrimSpace(b.Name)
	if b.GroupID == "" || !agentGroupNameRE.MatchString(name) {
		return errcode.E(errcode.INVALID, "group name")
	}
	if len(b.Description) > 256 {
		return errcode.E(errcode.INVALID, "description")
	}
	for id, g := range s.AgentGroups {
		if g.Name == name && id != b.GroupID {
			return errcode.E(errcode.CONFLICT, "group name already exists")
		}
	}
	cur := s.AgentGroups[b.GroupID]
	if cur.GroupID == "" {
		cur.GroupID = b.GroupID
		cur.CreatedUnix = b.NowUnix
	}
	cur.Name = name
	cur.Description = b.Description
	cur.UpdatedUnix = b.NowUnix
	s.AgentGroups[b.GroupID] = cur
	return nil
}

func (s *State) applyGroupDelete(b GroupDeleteBody) error {
	if _, ok := s.AgentGroups[b.GroupID]; !ok {
		return errcode.E(errcode.NOT_FOUND, "group not found")
	}
	for _, bind := range s.Bindings {
		if bind.Scope == ScopeAgentGroup && bind.ScopeID == b.GroupID {
			return errcode.E(errcode.CONFLICT, "group still has role bindings")
		}
	}
	delete(s.AgentGroups, b.GroupID)
	return nil
}

func (s *State) applyGroupMemberAdd(b GroupMemberBody) error {
	g, ok := s.AgentGroups[b.GroupID]
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "group not found")
	}
	m, ok := s.Members[b.NodeID]
	if !ok || m.Status != MemberAdmitted {
		return errcode.E(errcode.INVALID, "node is not an admitted member")
	}
	for _, id := range g.MemberIDs {
		if id == b.NodeID {
			return nil
		}
	}
	g.MemberIDs = append(g.MemberIDs, b.NodeID)
	s.AgentGroups[b.GroupID] = g
	return nil
}

func (s *State) applyGroupMemberRemove(b GroupMemberBody) error {
	g, ok := s.AgentGroups[b.GroupID]
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "group not found")
	}
	out := g.MemberIDs[:0]
	for _, id := range g.MemberIDs {
		if id != b.NodeID {
			out = append(out, id)
		}
	}
	g.MemberIDs = append([]string(nil), out...)
	s.AgentGroups[b.GroupID] = g
	return nil
}

func (s *State) NodeInGroup(nodeID, groupID string) bool {
	g, ok := s.AgentGroups[groupID]
	if !ok {
		return false
	}
	for _, id := range g.MemberIDs {
		if id == nodeID {
			return true
		}
	}
	return false
}
```

`Apply` switch 加上四个 cmd。`ensure` 必须在 `Restore` 后创建空 map（已有 `s.ensure()`）。

- [x] **Step 4: Run tests**

Run: `go test ./internal/control -count=1`

Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/control/command.go internal/control/fsm.go internal/control/fsm_test.go
git commit -m "feat(control): AgentGroup Raft commands"
```

---

### Task 4: Remove 节点时摘掉组成员

**Files:**
- Modify: `internal/control/fsm.go` `applyMemberRemove`
- Test: `internal/control/fsm_test.go`

- [x] **Step 1: Write the failing test**

```go
func TestFSM_MemberRemoveStripsGroups(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	_ = s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "node-a", CertSerial: "AA"}), now)
	_ = s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "g-fin", Name: "finance", NowUnix: now.Unix()}), now)
	_ = s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-a"}), now)
	if err := s.Apply(mustEncode(t, "member_remove", control.MemberRemoveBody{NodeID: "node-a"}), now); err != nil {
		t.Fatal(err)
	}
	if s.NodeInGroup("node-a", "g-fin") {
		t.Fatal("removed node must leave groups")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/control -run TestFSM_MemberRemoveStripsGroups -count=1`

Expected: FAIL（成员仍在组内）

- [x] **Step 3: Write minimal implementation**

在 `applyMemberRemove` 末尾、改 Status/CRL 之后：

```go
for id, g := range s.AgentGroups {
	filtered := g.MemberIDs[:0]
	changed := false
	for _, n := range g.MemberIDs {
		if n == b.NodeID {
			changed = true
			continue
		}
		filtered = append(filtered, n)
	}
	if changed {
		g.MemberIDs = append([]string(nil), filtered...)
		s.AgentGroups[id] = g
	}
}
```

注意：`filtered := g.MemberIDs[:0]` 会复用原切片；写成 `var filtered []string` 更安全。

- [x] **Step 4: Run tests**

Run: `go test ./internal/control -count=1`

Expected: PASS（旧的 `TestFSM_*MemberRemove*` 仍过）

- [x] **Step 5: Commit**

```bash
git add internal/control/fsm.go internal/control/fsm_test.go
git commit -m "feat(control): strip AgentGroup members on node remove"
```

---

### Task 5: CheckTarget（AGENT_GROUP / PROCESS_GROUP）

**Files:**
- Modify: `internal/control/fsm.go`
- Test: `internal/control/fsm_test.go`

- [x] **Step 1: Write the failing test**

```go
func TestFSM_CheckAgentGroupAndProcessGroup(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	_ = s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "node-fin"}), now)
	_ = s.Apply(mustEncode(t, "member_put", control.MemberPutBody{NodeID: "node-ads"}), now)
	_ = s.Apply(mustEncode(t, "group_put", control.GroupPutBody{GroupID: "g-fin", Name: "finance", NowUnix: now.Unix()}), now)
	_ = s.Apply(mustEncode(t, "group_member_add", control.GroupMemberBody{GroupID: "g-fin", NodeID: "node-fin"}), now)
	_ = s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u-fin", Username: "finop", PasswordHash: "h"}), now)
	_ = s.Apply(mustEncode(t, "bind_put", control.BindPutBody{
		UserID: "u-fin", RoleID: "operator", Scope: control.ScopeAgentGroup, ScopeID: "g-fin",
	}), now)
	_ = s.Apply(mustEncode(t, "user_put", control.UserPutBody{ID: "u-pg", Username: "pgop", PasswordHash: "h"}), now)
	_ = s.Apply(mustEncode(t, "bind_put", control.BindPutBody{
		UserID: "u-pg", RoleID: "operator", Scope: control.ScopeProcessGroup, ScopeID: "finance",
	}), now)

	if !s.CheckTarget("u-fin", "process.restart", control.CheckTarget{NodeID: "node-fin"}) {
		t.Fatal("agent group should allow finance node")
	}
	if s.CheckTarget("u-fin", "process.restart", control.CheckTarget{NodeID: "node-ads"}) {
		t.Fatal("agent group must not allow ads node")
	}
	if !s.CheckTarget("u-pg", "process.restart", control.CheckTarget{NodeID: "node-ads", ProcessGroup: "finance"}) {
		t.Fatal("process group matches spec.group regardless of node")
	}
	if s.CheckTarget("u-pg", "process.restart", control.CheckTarget{NodeID: "node-ads", ProcessGroup: "ads"}) {
		t.Fatal("process group must not match other name")
	}
	if s.CheckTarget("u-pg", "process.restart", control.CheckTarget{NodeID: "node-ads"}) {
		t.Fatal("empty process group must not match PROCESS_GROUP")
	}
	if !s.CanAny("u-fin", "process.read") {
		t.Fatal("CanAny")
	}
}
```

`fsm_test.go` 是 `package control_test`，只使用导出方法：`Check` / `CheckTarget` / `CanAny` / `NodeInGroup` / 导出字段 `AgentGroups`。

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/control -run TestFSM_CheckAgentGroupAndProcessGroup -count=1`

Expected: FAIL（无 `CheckTarget` / `CanAny`）

- [x] **Step 3: Write minimal implementation**

```go
type CheckTarget struct {
	NodeID       string
	ProcessGroup string
}

func (s *State) Check(userID, perm, targetNodeID string) bool {
	return s.CheckTarget(userID, perm, CheckTarget{NodeID: targetNodeID})
}

func (s *State) CheckTarget(userID, perm string, t CheckTarget) bool {
	u, ok := s.userByID(userID)
	if !ok || u.Status != UserActive {
		return false
	}
	for _, b := range s.Bindings {
		if b.UserID != userID {
			continue
		}
		role, ok := s.Roles[b.RoleID]
		if !ok || !roleHasPerm(role, perm) {
			continue
		}
		switch b.Scope {
		case ScopeCluster:
			return true
		case ScopeAgent:
			if t.NodeID != "" && b.ScopeID == t.NodeID {
				return true
			}
		case ScopeAgentGroup:
			if t.NodeID != "" && s.NodeInGroup(t.NodeID, b.ScopeID) {
				return true
			}
		case ScopeProcessGroup:
			if t.ProcessGroup != "" && t.ProcessGroup == b.ScopeID {
				return true
			}
		}
	}
	return false
}

func (s *State) CanAny(userID, perm string) bool {
	u, ok := s.userByID(userID)
	if !ok || u.Status != UserActive {
		return false
	}
	for _, b := range s.Bindings {
		if b.UserID != userID {
			continue
		}
		role, ok := s.Roles[b.RoleID]
		if ok && roleHasPerm(role, perm) {
			return true
		}
	}
	return false
}
```

把原来的 `Check` 方法替换为上述包装（旧测试继续用三参数 `Check`）。

- [x] **Step 4: Run tests**

Run: `go test ./internal/control -count=1`

Expected: PASS（`TestFSM_CheckAgentScope` 等旧测试仍过）

- [x] **Step 5: Commit**

```bash
git add internal/control/fsm.go internal/control/fsm_test.go
git commit -m "feat(control): RBAC AGENT_GROUP and PROCESS_GROUP scopes"
```

---

### Task 6: 内置权限与 ensure 同步

**Files:**
- Modify: `internal/auth/perm.go`
- Modify: `internal/control/fsm.go` (`allPermissions` / operator / viewer / `ensure`)
- Modify: `internal/control/fsm_test.go` (`allPerms` / `clusterAdminPerms` / `operatorPerms` / `viewerPerms`)
- Modify: `internal/api/role.go` `knownPerm`

- [x] **Step 1: Write the failing test**

```go
func TestFSM_EnsureSyncsBuiltinAlertPerms(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := mustBootstrap(t, now)
	r := s.Roles["operator"]
	r.Perms = []string{"cluster.read"} // 模拟升级前旧种子
	s.Roles["operator"] = r
	s.EnsureForTest()
	if !hasPerm(s.Roles["operator"].Perms, "batch.execute") {
		t.Fatal("operator should gain batch.execute")
	}
	if !hasPerm(s.Roles["viewer"].Perms, "alert.read") {
		t.Fatal("viewer should gain alert.read")
	}
	if hasPerm(s.Roles["operator"].Perms, "alert.manage") {
		t.Fatal("operator must not gain alert.manage")
	}
}

func hasPerm(perms []string, want string) bool {
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}
```

`ensure` 未导出。在 `fsm.go` 增加测试用包装：

```go
func (s *State) EnsureForTest() { s.ensure() }
```

只给 `control_test` 用，不要在生产路径另造同步入口（生产仍走 `Apply`/`Restore` 里的 `ensure`）。

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/control -run TestFSM_EnsureSyncsBuiltinAlertPerms -count=1`

Expected: FAIL

- [x] **Step 3: Write minimal implementation**

`internal/auth/perm.go` 增加：

```go
PermBatchExecute = "batch.execute"
PermAlertRead    = "alert.read"
PermAlertManage  = "alert.manage"
PermBackupRead   = "backup.read"
PermBackupManage = "backup.manage"
```

`fsm.go` `allPermissions` 追加上述五个字符串（放在 `audit.read` 之后、`command.execute` 之前）。

`clusterAdminPermissions` 已按黑名单过滤 `user.delete` / `role.manage` / `command.*`，新 perm 会自动进入 Cluster Admin。

`operatorPermissions` 追加 `"batch.execute"`, `"alert.read"`。

`viewerPermissions` 追加 `"alert.read"`。

`ensure()` 末尾：

```go
for _, r := range builtinRoles() {
	s.Roles[r.ID] = r
}
```

注意：这会在每次 Apply 开头 `ensure()` 时重置四个内置角色。与 spec「自定义 role 不改」一致。确认 `Apply` 已调用 `s.ensure()`——是的。

`knownPerm` 加上五个新常量。

更新 `fsm_test.go` 的 `allPerms` / `clusterAdminPerms` / `operatorPerms` / `viewerPerms` 以免 `TestFSM_CheckSuperAdminAllowsAll` 与角色种子测试失败。

`clusterAdminPerms` 测试列表需含 `batch.execute`、`alert.read`、`alert.manage`、`backup.read`、`backup.manage`。

- [x] **Step 4: Run tests**

Run: `go test ./internal/control ./internal/auth ./internal/api -count=1`

Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/auth/perm.go internal/control/fsm.go internal/control/fsm_test.go internal/api/role.go
git commit -m "feat(auth): add V1.1 perms and resync builtin roles"
```

---

### Task 7: GrantRole 接受新 scope

**Files:**
- Modify: `internal/api/role.go` `parseScope`
- Test: `internal/api/role_test.go`

- [x] **Step 1: Write the failing test**

在现有 Grant 测试旁追加新函数：

```go
func TestRoleAPI_GrantGroupScopes(t *testing.T) {
	ctx := context.Background()
	_, svc := newBootstrappedAuth(t)
	api := &RoleAPI{Auth: svc}
	now := time.Unix(1_700_000_000, 0)
	applyAuthCmd(t, svc, control.CmdMemberPut, control.MemberPutBody{NodeID: "node-1"})
	applyAuthCmd(t, svc, control.CmdGroupPut, control.GroupPutBody{GroupID: "g-fin", Name: "finance", NowUnix: now.Unix()})

	created, err := api.CreateRole(ctx, connect.NewRequest(&procmeshv1.CreateRoleRequest{
		Meta:        &procmeshv1.MutationMeta{OperationId: "op-role", Operator: "t"},
		Name:        "ops",
		Permissions: []string{auth.PermProcessRead},
	}))
	if err != nil {
		t.Fatal(err)
	}
	role := created.Msg.GetRole()

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-ag", Operator: "t"},
		UserId: "user-admin", RoleId: role.GetRoleId(),
		ScopeType: "AGENT_GROUP", ScopeId: "g-fin",
	}))
	if err != nil {
		t.Fatal(err)
	}

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-ag-miss", Operator: "t"},
		UserId: "user-admin", RoleId: role.GetRoleId(),
		ScopeType: "AGENT_GROUP", ScopeId: "missing",
	}))
	assertInvalidOrNotFound(t, err)

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-pg", Operator: "t"},
		UserId: "user-admin", RoleId: role.GetRoleId(),
		ScopeType: "PROCESS_GROUP", ScopeId: "finance",
	}))
	if err != nil {
		t.Fatal(err)
	}

	_, err = api.GrantRole(ctx, connect.NewRequest(&procmeshv1.GrantRoleRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-pg-bad", Operator: "t"},
		UserId: "user-admin", RoleId: role.GetRoleId(),
		ScopeType: "PROCESS_GROUP", ScopeId: "bad name",
	}))
	assertInvalidMsg(t, err, "scope_id")
}
```

`GROUP` 仍必须 `invalid scope_type`（旧测试保留）。

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api -run TestRoleAPI_GrantGroupScopes -count=1`

Expected: FAIL（`parseScope` 拒 AGENT_GROUP）

- [x] **Step 3: Write minimal implementation**

```go
func parseScope(scopeType, scopeID string) (control.ScopeType, error) {
	s := strings.ToUpper(strings.TrimSpace(scopeType))
	if s == "" {
		s = string(control.ScopeCluster)
	}
	switch control.ScopeType(s) {
	case control.ScopeCluster:
		return control.ScopeCluster, nil
	case control.ScopeAgent:
		if scopeID == "" {
			return "", ToConnect(errcode.E(errcode.INVALID, "scope_id required for AGENT"))
		}
		return control.ScopeAgent, nil
	case control.ScopeAgentGroup:
		if scopeID == "" {
			return "", ToConnect(errcode.E(errcode.INVALID, "scope_id required for AGENT_GROUP"))
		}
		return control.ScopeAgentGroup, nil
	case control.ScopeProcessGroup:
		id := strings.TrimSpace(scopeID)
		if !processGroupNameOK(id) {
			return "", ToConnect(errcode.E(errcode.INVALID, "scope_id"))
		}
		return control.ScopeProcessGroup, nil
	default:
		return "", ToConnect(errcode.E(errcode.INVALID, "invalid scope_type"))
	}
}

func processGroupNameOK(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}
```

在 `GrantRole` 里，`parseScope` 成功后：

```go
if scope == control.ScopeAgentGroup {
	if _, ok := st.AgentGroups[req.Msg.GetScopeId()]; !ok {
		return nil, ToConnect(errcode.E(errcode.NOT_FOUND, "agent group not found"))
	}
}
```

`st` 已在后面取；把 `st := s.Auth.Store().View()` 上移到 parse 之后。

- [x] **Step 4: Run tests**

Run: `go test ./internal/api -run 'TestRoleAPI_' -count=1`

Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/api/role.go internal/api/role_test.go
git commit -m "feat(api): grant AGENT_GROUP and PROCESS_GROUP bindings"
```

---

### Task 8: proto — GroupService 与摘要字段

**Files:**
- Modify: `proto/procmesh/v1/api.proto`
- Generate: `make proto` 与 `make proto-ts`（需 `cd web && npm ci` 若缺插件）

- [x] **Step 1: Edit proto（无单独红测；下一步 API 测试会红）**

`ProcessSummary` 增加：

```protobuf
message ProcessSummary {
  string name = 1;
  string desired = 2;
  string observed = 3;
  string health = 4;
  int64 latest_revision = 5;
  int64 active_revision = 6;
  int64 freshness_unix_ms = 7;
  string process_id = 8;
  string group = 9;
}
```

`Node` 增加：

```protobuf
  repeated string agent_group_ids = 15;
```

`ListProcessesRequest`：

```protobuf
message ListProcessesRequest {
  string group = 1; // optional process group name filter
}
```

`Binding` / `GrantRoleRequest` 注释改为 `CLUSTER | AGENT | AGENT_GROUP | PROCESS_GROUP`。

在 `RoleService` 之后追加：

```protobuf
message AgentGroup {
  string group_id = 1;
  string name = 2;
  string description = 3;
  repeated string member_node_ids = 4;
  int64 created_unix = 5;
  int64 updated_unix = 6;
}

message ListAgentGroupsRequest {}
message ListAgentGroupsResponse { repeated AgentGroup groups = 1; }

message CreateAgentGroupRequest {
  MutationMeta meta = 1;
  string name = 2;
  string description = 3;
}
message CreateAgentGroupResponse { AgentGroup group = 1; }

message DeleteAgentGroupRequest {
  MutationMeta meta = 1;
  string group_id = 2;
}
message DeleteAgentGroupResponse {}

message AgentGroupMemberRequest {
  MutationMeta meta = 1;
  string group_id = 2;
  string node_id = 3;
}
message AgentGroupMemberResponse { AgentGroup group = 1; }

service GroupService {
  rpc ListAgentGroups(ListAgentGroupsRequest) returns (ListAgentGroupsResponse);
  rpc CreateAgentGroup(CreateAgentGroupRequest) returns (CreateAgentGroupResponse);
  rpc DeleteAgentGroup(DeleteAgentGroupRequest) returns (DeleteAgentGroupResponse);
  rpc AddAgentGroupMember(AgentGroupMemberRequest) returns (AgentGroupMemberResponse);
  rpc RemoveAgentGroupMember(AgentGroupMemberRequest) returns (AgentGroupMemberResponse);
}
```

- [x] **Step 2: Generate**

Run:

```bash
make proto
cd web && npm ci && cd .. && make proto-ts
```

Expected: 生成物更新且 `go build ./...` 通过（尚未实现 handler，`var _ Handler` 还没加，build 应仍过）。

- [x] **Step 3: Commit proto + generated**

```bash
git add proto/procmesh/v1/api.proto proto/procmesh/v1/api.pb.go proto/procmesh/v1/procmeshv1connect/api.connect.go web/src/gen
git commit -m "feat(proto): GroupService and group summary fields"
```

本任务无独立测试；handler 在 Task 9。

---

### Task 9: GroupAPI

**Files:**
- Create: `internal/api/group.go`
- Create: `internal/api/group_test.go`
- Modify: `internal/api/server.go`

- [x] **Step 1: Write the failing test**

`internal/api/group_test.go` 仿 `role_test.go` 的 auth fixture：

```go
func TestGroupAPI_CRUDAndRBAC(t *testing.T) {
	e := newRBACEnv(t)
	applyAuthCmd(t, e.svc, control.CmdMemberPut, control.MemberPutBody{NodeID: "node-1"})
	adminSid := e.loginAs(t, "admin", testAdminPass)
	gcli := procmeshv1connect.NewGroupServiceClient(e.http, e.url)
	ctx := context.Background()

	created, err := gcli.CreateAgentGroup(ctx, bearerReq(adminSid, &procmeshv1.CreateAgentGroupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-cg", Operator: "t"},
		Name: "finance",
	}))
	if err != nil {
		t.Fatal(err)
	}
	gid := created.Msg.GetGroup().GetGroupId()
	if gid == "" || created.Msg.GetGroup().GetName() != "finance" {
		t.Fatalf("%+v", created.Msg.GetGroup())
	}

	_, err = gcli.AddAgentGroupMember(ctx, bearerReq(adminSid, &procmeshv1.AgentGroupMemberRequest{
		Meta:    &procmeshv1.MutationMeta{OperationId: "op-add", Operator: "t"},
		GroupId: gid, NodeId: "node-1",
	}))
	if err != nil {
		t.Fatal(err)
	}

	list, err := gcli.ListAgentGroups(ctx, bearerReq(adminSid, &procmeshv1.ListAgentGroupsRequest{}))
	if err != nil || len(list.Msg.GetGroups()) != 1 {
		t.Fatalf("list %+v err=%v", list, err)
	}
	if len(list.Msg.GetGroups()[0].GetMemberNodeIds()) != 1 {
		t.Fatal("member missing")
	}

	viewSid := e.loginAs(t, "viewer", testAdminPass)
	_, err = gcli.CreateAgentGroup(ctx, bearerReq(viewSid, &procmeshv1.CreateAgentGroupRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-den", Operator: "v"},
		Name: "other",
	}))
	if err == nil {
		t.Fatal("viewer must be denied")
	}
}
```

`newRBACEnv` / `loginAs` / `bearerReq` / `applyAuthCmd` / `testAdminPass` 已在 `internal/api/rbac_test.go` 与 `authn_test.go`。Create 无 Principal 时 `requirePerm` 会放行，所以必须走 HTTP + Bearer（完整 Server），不能只 new `GroupAPI{}`。

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api -run TestGroupAPI_CRUDAndRBAC -count=1`

Expected: FAIL（无 `GroupAPI`）

- [x] **Step 3: Write minimal implementation**

`internal/api/group.go`：

```go
type GroupAPI struct{ Auth *auth.Service }

func (s *GroupAPI) ListAgentGroups(ctx context.Context, _ *connect.Request[procmeshv1.ListAgentGroupsRequest]) (*connect.Response[procmeshv1.ListAgentGroupsResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermNodeRead, "", false, true); err != nil {
		return nil, err
	}
	st := s.Auth.Store().View()
	ids := make([]string, 0, len(st.AgentGroups))
	for id := range st.AgentGroups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := &procmeshv1.ListAgentGroupsResponse{}
	for _, id := range ids {
		out.Groups = append(out.Groups, agentGroupToProto(st.AgentGroups[id]))
	}
	return connect.NewResponse(out), nil
}

func (s *GroupAPI) CreateAgentGroup(ctx context.Context, req *connect.Request[procmeshv1.CreateAgentGroupRequest]) (*connect.Response[procmeshv1.CreateAgentGroupResponse], error) {
	if err := requireAuthConfigured(s.Auth); err != nil {
		return nil, err
	}
	if err := requirePerm(ctx, s.Auth, auth.PermNodeManage, "", true, true); err != nil {
		return nil, err
	}
	if _, _, err := metaOf(req.Msg.GetMeta()); err != nil {
		return nil, err
	}
	id, err := newAuthID()
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := applyAuth(s.Auth, control.CmdGroupPut, control.GroupPutBody{
		GroupID: id, Name: req.Msg.GetName(), Description: req.Msg.GetDescription(),
		NowUnix: time.Now().Unix(),
	}); err != nil {
		return nil, err
	}
	g := s.Auth.Store().View().AgentGroups[id]
	return connect.NewResponse(&procmeshv1.CreateAgentGroupResponse{Group: agentGroupToProto(g)}), nil
}
```

`DeleteAgentGroup` / `AddAgentGroupMember` / `RemoveAgentGroupMember` 同样：`node.manage` + `metaOf` + `applyAuth`。Add 前不在 API 重复校验成员（FSM 已校验）。Delete 走 FSM 的 binding CONFLICT。

`server.go` 在 Role handler 旁：

```go
	gp, gh := procmeshv1connect.NewGroupServiceHandler(&GroupAPI{Auth: opts.Auth}, intercept)
	mountConnect(engine, gp, gh)
```

插在 `RoleService` 的 `mountConnect` 之后、`AuditService` 之前。

- [x] **Step 4: Run tests**

Run: `go test ./internal/api -count=1`

Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/api/group.go internal/api/group_test.go internal/api/server.go
git commit -m "feat(api): GroupService for Agent Groups"
```

---

### Task 10: Node.agent_group_ids 与 List/Get 过滤

**Files:**
- Modify: `internal/api/node.go`
- Modify: `internal/api/rbac.go`
- Test: `internal/api/node_test.go`（已有则追加；否则 `node_rbac_test.go`）

- [x] **Step 1: Write the failing test**

```go
func TestNodeAPI_FiltersByAgentGroup(t *testing.T) {
	// 两节点摘要 + 一组只含 node-fin
	// finance operator principal
	// ListNodes 只能看到 node-fin
	// GetNode(node-ads) DENIED 或 NOT_FOUND（锁定：DENIED，避免探测）
}
```

夹具：`NodeAPI{Deps: fake members, Auth: svc}`。成员摘要含 `NodeID: node-fin` 与 `node-ads`。Raft 里 `g-fin` 只含 `node-fin`，用户绑定 `operator` + `AGENT_GROUP/g-fin`。

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api -run TestNodeAPI_FiltersByAgentGroup -count=1`

Expected: FAIL（ListNodes 用空 target 的 `node.read`，AGENT_GROUP 用户被整体 DENIED 或看到全部）

- [x] **Step 3: Write minimal implementation**

`auth/rbac.go`：

```go
func (s *Service) Allow(p Principal, perm, targetNodeID string) error {
	return s.AllowOn(p, perm, control.CheckTarget{NodeID: targetNodeID})
}

func (s *Service) AllowOn(p Principal, perm string, t control.CheckTarget) error {
	st, err := s.storeOrErr()
	if err != nil {
		return err
	}
	view := st.View()
	if !view.CheckTarget(p.UserID, perm, t) {
		return errcode.E(errcode.DENIED, "permission denied")
	}
	return nil
}

func (s *Service) AllowAny(p Principal, perm string) error {
	st, err := s.storeOrErr()
	if err != nil {
		return err
	}
	if !st.View().CanAny(p.UserID, perm) {
		return errcode.E(errcode.DENIED, "permission denied")
	}
	return nil
}
```

`AllowWrite` 末尾改为 `s.AllowOn`（仍用 `CheckTarget{NodeID: targetNodeID}`）。

`api/rbac.go` 增加：

```go
func requireAnyPerm(ctx context.Context, svc *auth.Service, perm string) error {
	if svc == nil {
		return nil
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil
	}
	if err := svc.AllowAny(p, perm); err != nil {
		return ToConnect(err)
	}
	return nil
}

func requirePermOn(ctx context.Context, svc *auth.Service, perm string, t control.CheckTarget, write, local bool) error {
	if svc == nil {
		return nil
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil
	}
	if write {
		if err := svc.AllowWrite(p, perm, t.NodeID, local); err != nil {
			return ToConnect(err)
		}
		// AllowWrite 只用 NodeID。写路径在 process handler 再调 AllowOn 含 group。
		if t.ProcessGroup != "" {
			if err := svc.AllowOn(p, perm, t); err != nil {
				return ToConnect(err)
			}
		}
		return nil
	}
	if err := svc.AllowOn(p, perm, t); err != nil {
		return ToConnect(err)
	}
	return nil
}
```

写路径双检有点绕。更干净：扩展 `AllowWriteOn(p, perm, t, local)`，内部 quorum 逻辑与 `AllowWrite` 相同，鉴权用 `CheckTarget`。

```go
func (s *Service) AllowWriteOn(p Principal, perm string, t control.CheckTarget, local bool) error {
	st, err := s.storeOrErr()
	if err != nil {
		return err
	}
	if isControlPlaneWrite(perm) && !st.HasQuorum() {
		return errcode.E(errcode.UNAVAILABLE, "control quorum lost")
	}
	if !local && isMutation(perm) && !st.HasQuorum() {
		if !st.CacheFresh(st.View().Policy.RBACCacheTTL) {
			return errcode.E(errcode.DENIED, "rbac cache expired")
		}
	}
	return s.AllowOn(p, perm, t)
}

func (s *Service) AllowWrite(p Principal, perm, targetNodeID string, local bool) error {
	return s.AllowWriteOn(p, perm, control.CheckTarget{NodeID: targetNodeID}, local)
}
```

`ListNodes`：`requireAnyPerm(..., PermNodeRead)`，然后对每个 node：若 `Auth != nil` 且有 principal，用 `CheckTarget{NodeID: n.NodeID}` 过滤。`nodeToProto` 填 `AgentGroupIds`：遍历 `Auth.Store().View().AgentGroups`，把包含该 node 的 `group_id` 按排序放入。无 Auth 时字段空。

`GetNode`：找到节点后 `requirePermOn(..., CheckTarget{NodeID: n.NodeID}, false, true)`。无权限 → `DENIED`。

- [x] **Step 4: Run tests**

Run: `go test ./internal/auth ./internal/api -count=1`

Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/auth/rbac.go internal/auth/rbac_test.go internal/api/rbac.go internal/api/node.go internal/api/node_test.go
git commit -m "feat(api): filter nodes by AGENT_GROUP and attach group ids"
```

---

### Task 11: Process API 按 group 鉴权与过滤

**Files:**
- Modify: `internal/api/process.go`
- Test: `internal/api/process_test.go` 或新建 `internal/api/process_rbac_group_test.go`

- [x] **Step 1: Write the failing test**

单节点 Manager 上两个 spec：`api` group=`finance`，`ads` group=`adsys`。用户只有 `PROCESS_GROUP=finance` + operator。

断言：

- `ListProcesses` 只返回 `api`
- `ListProcesses` + `group=adsys` 返回空（不是 DENIED）
- `GetProcess(ads)` / `RestartProcess(ads)` → DENIED
- `GetProcess(api)` / `RestartProcess(api)` 成功

入口侧远程：用 gossip summary 的 group 做 `CheckTarget`；Owner 侧用 `spec.Group`。

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api -run TestProcessAPI_ProcessGroupScope -count=1`

Expected: FAIL（List 全量或 Restart 不看 group）

- [x] **Step 3: Write minimal implementation**

`ListProcesses` 本地分支：

1. `requireAnyPerm(ctx, s.Auth, auth.PermProcessRead)` 替代 `requireRoutePerm`（远程 hop 仍先 `requireAnyPerm`，转发后 Owner 再滤）。
2. 对每个 spec：`filter := req.Msg.GetGroup(); if filter != "" && spec.Group != filter { continue }`
3. 再 `AllowOn(p, process.read, {NodeID: local/owner, ProcessGroup: spec.Group})`，失败则 skip，不报错。

`GetProcess` / Start / Stop / Restart / Kill / Apply / Delete / Config：在 resolve spec 之后（本地）或从 route+gossip 取 group（远程）调用 `AllowOn`/`AllowWriteOn` 带 `ProcessGroup`。

远程入口取 group：

```go
func gossipGroup(router *Router, nodeID, processName string) string {
	if router == nil {
		return ""
	}
	// Router 上若已有 members 快照，找 node.Processes 里 Name == processName 的 Group
}
```

若 `Router` 没有导出 members，从 `ProcessAPI` 增加可选 `Members func() []cluster.NodeSummary`（与 ClusterAPI 相同）。没有摘要则 `ProcessGroup=""`，仅 CLUSTER/AGENT/AGENT_GROUP 能过；纯 PROCESS_GROUP 用户在入口会被拒，Owner 本不会被误授权。

Apply 创建：用 `req.Msg.GetSpec().GetGroup()` 作为 target group。

- [x] **Step 4: Run tests**

Run: `go test ./internal/api ./internal/process -count=1`

Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/api/process.go internal/api/process_rbac_group_test.go
git commit -m "feat(api): enforce PROCESS_GROUP on process list and mutations"
```

---

### Task 12: CLI

**Files:**
- Create: `internal/cli/group.go`
- Modify: `internal/cli/root.go`（usage + switch + flags）
- Modify: `internal/cli/client.go`
- Modify: `internal/cli/user.go`（`--scope` 帮助）
- Test: `internal/cli/root_test.go`（若有 parse 测试则扩）

- [x] **Step 1: Write the failing test**

`internal/cli/root_test.go` 增加 usage 包含 `group list`，以及 `parseArgs` 识别：

```
group create --name finance
group add-member --group-id GID --node-id NID
role grant --scope AGENT_GROUP --scope-id GID
```

若现有测试只查 `usageText` 字符串，把新行加进 `usageText` 后先让「未知命令 group」的解析测试红。

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli -count=1`

Expected: FAIL（usage 缺 group 或 parse 失败）

- [x] **Step 3: Write minimal implementation**

`usageText` 增加：

```
  group list
  group create --name NAME [--description T]
  group delete GROUP_ID
  group add-member --group-id ID --node-id ID
  group remove-member --group-id ID --node-id ID
```

并把 `role grant` 的 scope 写成 `CLUSTER|AGENT|AGENT_GROUP|PROCESS_GROUP`。

`options` 增加 `groupID string`（若与现有字段冲突则复用 `scopeID` / 新字段 `nodeIDMember`）。

`parseArgs` 增加 `--name`（已有）、`--description`、`--group-id`、`--node-id`（`--node-id` 与 `--node` 不同：后者是 target owner）。

`client.group` = `NewGroupServiceClient`。

`internal/cli/group.go`：`runGroup` switch list/create/delete/add-member/remove-member，打印 `group_id=` `name=` `members=` 纯文本，风格同 `user.go`。

`Main` switch 加 `case "group":`。

- [x] **Step 4: Run tests**

Run: `go test ./internal/cli -count=1`

Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): group commands and expanded role grant scopes"
```

---

### Task 13: Web Groups 页、授权 UI、筛选

**Files:**
- Modify: `web/src/lib/rpc.ts`
- Modify: `web/src/router.ts`
- Modify: `web/src/components/AppShell.vue`
- Create: `web/src/pages/GroupsPage.vue`
- Create: `web/src/pages/GroupsPage.test.ts`
- Modify: `web/src/pages/RolesPage.vue` + `RolesPage.test.ts`
- Modify: `web/src/pages/ProcessesPage.vue`（按 `group` query 筛；调用 `listProcesses({ group })` 若已有 list client）
- Modify: `web/public/locales/en/common.json`、`web/public/locales/zh/common.json`
- 可能：`web/src/pages/NodesPage.vue` 显示 `agentGroupIds`

- [x] **Step 1: Write the failing test**

`GroupsPage.test.ts`：mount 页面，mock `GroupService.listAgentGroups` 返回一个组，断言名字可见；有 `node.manage` 时显示 Create。

`RolesPage.test.ts`：授权 scope 下拉含 `AGENT_GROUP` / `PROCESS_GROUP`。

`AppShell.test.ts`：有 `node.read` 时出现 Groups 导航。

- [x] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- GroupsPage RolesPage AppShell`

Expected: FAIL（无页面 / 无 scope）

- [x] **Step 3: Write minimal implementation**

- `rpc.ts`：`GroupService` client，`useGroupClient()`。
- `router.ts`：`{ path: "groups", component: GroupsPage }`。
- `AppShell.vue`：`FolderTree`（lucide）导航 `{ to: "/groups", label: t("nav.groups") }`，在 Processes 后、Users 前；权限 `node.read`。
- `GroupsPage.vue`：表格（name、members、actions）；Create 表单 name+description；行内 Add/Remove member（node_id 输入）；Delete 调用 delete。`node.manage` 才显示写按钮。
- `RolesPage.vue`：`grantScope` 类型扩为四值；`AGENT_GROUP` / `PROCESS_GROUP` 时 `scope_id` 必填；label 用 i18n。
- `ROLE_PERMISSIONS` 数组追加五个新 perm（与 Task 6 一致）。
- `ProcessesPage`：若已有 list，加 group 过滤输入，传 `listProcesses({ group })`。没有 list client 就只在前端对已有行按 `process.group` 滤（ProcessView 已有 proto `group` 字段）。
- locales：`nav.groups`、`group.create`、`group.members`、`role.scopeAgentGroup`、`role.scopeProcessGroup`。
- `cd web && npm run i18n:check`

- [x] **Step 4: Run tests**

Run:

```bash
cd web && npm test && npm run i18n:check
```

Expected: PASS

- [x] **Step 5: Commit**

```bash
git add web
git commit -m "feat(web): Agent Groups page and group-scoped role grants"
```

---

### Task 14: Q1 验收 — Finance Operator

**Files:**
- Create: `internal/agent/q1_accept_test.go`

- [x] **Step 1: Write the failing test**

```go
func TestQ1_FinanceOperatorCannotTouchAds(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	initAndLogin(t, addr)

	fin := writeSpecWithGroup(t, "pay", "finance")
	ads := writeSpecWithGroup(t, "ad", "ads")
	mustCLI(t, addr, "process", "apply", "--file", fin, "--expected-revision", "0")
	mustCLI(t, addr, "process", "apply", "--file", ads, "--expected-revision", "0")

	out := mustCLI(t, addr, "group", "create", "--name", "finance")
	gid := parseKV(out, "group_id")
	nodeID := currentNodeID(t, addr) // node list 解析，或 status
	mustCLI(t, addr, "group", "add-member", "--group-id", gid, "--node-id", nodeID)

	out = mustCLI(t, addr, "user", "create", "--user", "finop", "--password", "finop-pass1")
	uid := parseKV(out, "user_id")
	mustCLI(t, addr, "role", "grant", "--user-id", uid, "--role-id", "operator",
		"--scope", "PROCESS_GROUP", "--scope-id", "finance")

	mustCLI(t, addr, "login", "--user", "finop", "--password", "finop-pass1")

	code, out, errb := runP1CLI("--server", addr, "process", "list")
	if code != 0 {
		t.Fatalf("list: %s %s", errb, out)
	}
	if !strings.Contains(out, "pay") || strings.Contains(out, "ad") {
		t.Fatalf("list leaked ads: %s", out)
	}

	code, _, errb = runP1CLI("--server", addr, "process", "restart", "ad")
	if code == 0 || !strings.Contains(errb, "DENIED") {
		t.Fatalf("restart ads want DENIED, stderr=%q", errb)
	}

	code, _, errb = runP1CLI("--server", addr, "process", "restart", "pay")
	if code != 0 {
		t.Fatalf("restart pay: %s", errb)
	}
}
```

`writeSpecWithGroup` 复制 `writeSleepSpec` 并写 `group: finance`。`mustCLI` 封装 `runP1CLI` 非 0 就 Fatal。`currentNodeID`：`node list` 每行是 tab 分隔，第 1 列是 `node_id`（见 `formatNodeLine`）。取第一行第一列。

`writeSpecWithGroup`：

```go
func writeSpecWithGroup(t *testing.T, name, group string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".yaml")
	body := fmt.Sprintf("name: %s\ncommand: /bin/sleep\nargs:\n  - \"60\"\ninstances: 1\ngroup: %s\n", name, group)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
```

另写 `TestQ1_AgentGroupScope`（同一文件）：创建组、**不** add-member，grant `operator` + `AGENT_GROUP`，login 后 `process restart pay` 必须 DENIED；再 `group add-member`，同一用户 restart 成功。不启第二节点。

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent -run TestQ1_ -count=1 -timeout 120s`

Expected: FAIL（CLI group 或过滤未接通到真实 agent）

- [x] **Step 3: Fix wiring gaps only**

若失败原因是 Agent 没挂 GroupService：检查 `internal/agent` 是否用 `api.NewServer`（已在 Task 9 挂上）。若 CLI session 在 login 后没带 token，沿用 P4 的 `loginAdmin` / 会话文件逻辑。不要扩 scope。

- [x] **Step 4: Run tests**

Run:

```bash
go test ./internal/control ./internal/auth ./internal/process ./internal/api ./internal/cli ./internal/cluster ./internal/agent -run 'TestQ1_|TestFSM_AgentGroup|TestFSM_CheckAgentGroup|TestGroupAPI_|TestProcessAPI_ProcessGroup|TestNodeAPI_FiltersBy' -count=1 -timeout 180s
go test ./internal/control ./internal/auth ./internal/process -count=1
```

Expected: PASS；三包覆盖率 ≥ 80%

- [x] **Step 5: Commit**

```bash
git add internal/agent/q1_accept_test.go
git commit -m "test(agent): Q1 finance operator cannot touch other groups"
```

---

## 完成定义

Q1 可演示出口：

```text
procmesh group create --name finance
procmesh group add-member --group-id ... --node-id ...
procmesh role grant --user-id ... --role-id operator --scope PROCESS_GROUP --scope-id finance
```

以该用户登录后：只看/管 `group=finance` 的进程；Web `/groups` 可管理成员；Roles 页可授两种新 scope。

**不要开始 Q2。** Q2 plan 在本阶段合并后再写。
