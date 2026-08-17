package control

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/raft"
	"github.com/qleelulu/procmesh/internal/errcode"
)

const (
	MinPasswordLen      = 10
	SessionTTL          = 12 * time.Hour
	LockAfter           = 10
	LockFor             = 15 * time.Minute
	DefaultRBACCacheTTL = 5 * time.Minute
)

type ScopeType string

const (
	ScopeCluster      ScopeType = "CLUSTER"
	ScopeAgent        ScopeType = "AGENT"
	ScopeAgentGroup   ScopeType = "AGENT_GROUP"
	ScopeProcessGroup ScopeType = "PROCESS_GROUP"
)

var agentGroupNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type UserStatus string

const (
	UserActive   UserStatus = "ACTIVE"
	UserDisabled UserStatus = "DISABLED"
	UserLocked   UserStatus = "LOCKED"
)

type MemberStatus string

const (
	MemberAdmitted MemberStatus = "ADMITTED"
	MemberRemoved  MemberStatus = "REMOVED"
	MemberRevoked  MemberStatus = "REVOKED"
)

type User struct {
	ID, Username, PasswordHash, DisplayName, Email string
	Status                                         UserStatus
	CreatedUnix, LastLoginUnix, LockedUntilUnix    int64
	FailCount                                      int
}

type Role struct {
	ID, Name string
	Perms    []string
}

type Binding struct {
	UserID, RoleID, ScopeID string
	Scope                   ScopeType
}

type Session struct {
	ID, UserID, CSRF string
	ExpiresUnix      int64
}

type APIToken struct {
	ID, UserID, Name, Hash string
	ExpiresUnix            int64
	Revoked                bool
}

type JoinToken struct {
	ID, Hash    string
	ExpiresUnix int64
	Remaining   int
	Revoked     bool
}

type Member struct {
	NodeID, RaftAddr, CertSerial string
	Status                       MemberStatus
}

type Policy struct {
	RBACCacheTTL time.Duration
}

type State struct {
	ClusterID     string                  `json:"cluster_id"`
	Users         map[string]User         `json:"users"`       // by username
	UsersByID     map[string]string       `json:"users_by_id"` // id → username
	Roles         map[string]Role         `json:"roles"`
	Bindings      []Binding               `json:"bindings"`
	Sessions      map[string]Session      `json:"sessions"`
	APITokens     map[string]APIToken     `json:"api_tokens"`
	JoinTokens    map[string]JoinToken    `json:"join_tokens"`    // by id
	Members       map[string]Member       `json:"members"`        // by node_id
	CRL           map[string]struct{}     `json:"crl"`            // cert serial hex
	AgentGroups   map[string]AgentGroup   `json:"agent_groups"`   // by group_id
	AlertChannels map[string]AlertChannel `json:"alert_channels"` // by channel_id
	AlertPolicy   AlertPolicy             `json:"alert_policy"`
	Policy        Policy                  `json:"policy"`
}

func NewState() *State {
	s := &State{}
	s.ensure()
	return s
}

func (s *State) ensure() {
	if s.Users == nil {
		s.Users = map[string]User{}
	}
	if s.UsersByID == nil {
		s.UsersByID = map[string]string{}
	}
	if s.Roles == nil {
		s.Roles = map[string]Role{}
	}
	if s.Bindings == nil {
		s.Bindings = []Binding{}
	}
	if s.Sessions == nil {
		s.Sessions = map[string]Session{}
	}
	if s.APITokens == nil {
		s.APITokens = map[string]APIToken{}
	}
	if s.JoinTokens == nil {
		s.JoinTokens = map[string]JoinToken{}
	}
	if s.Members == nil {
		s.Members = map[string]Member{}
	}
	if s.CRL == nil {
		s.CRL = map[string]struct{}{}
	}
	if s.AgentGroups == nil {
		s.AgentGroups = map[string]AgentGroup{}
	}
	if s.AlertChannels == nil {
		s.AlertChannels = map[string]AlertChannel{}
	}
	if s.AlertPolicy.DedupWindowSec == 0 {
		s.AlertPolicy = DefaultAlertPolicy()
	}
	for _, r := range builtinRoles() {
		s.Roles[r.ID] = r
	}
}

// EnsureForTest exposes ensure() for tests. Production still only calls ensure from Apply/Restore.
func (s *State) EnsureForTest() { s.ensure() }

// Apply mutates in-memory control state. now is the apply wall clock.
func (s *State) Apply(cmd Command, now time.Time) error {
	s.ensure()
	switch cmd.Type {
	case CmdBootstrap:
		return applyJSON(cmd.Body, func(b BootstrapBody) error { return s.applyBootstrap(b) })
	case CmdUserPut:
		return applyJSON(cmd.Body, s.applyUserPut)
	case CmdUserDisable:
		return applyJSON(cmd.Body, s.applyUserDisable)
	case CmdLoginOK:
		return applyJSON(cmd.Body, func(b LoginOKBody) error { return s.applyLoginOK(b, now) })
	case CmdLoginFail:
		return applyJSON(cmd.Body, func(b LoginFailBody) error { return s.applyLoginFail(b, now) })
	case CmdSessionPut:
		return applyJSON(cmd.Body, func(b SessionPutBody) error { return s.applySessionPut(b, now) })
	case CmdSessionDel:
		return applyJSON(cmd.Body, s.applySessionDel)
	case CmdTokenPut:
		return applyJSON(cmd.Body, s.applyTokenPut)
	case CmdTokenRevoke:
		return applyJSON(cmd.Body, s.applyTokenRevoke)
	case CmdRolePut:
		return applyJSON(cmd.Body, s.applyRolePut)
	case CmdBindPut:
		return applyJSON(cmd.Body, s.applyBindPut)
	case CmdJoinTokenPut:
		return applyJSON(cmd.Body, func(b JoinTokenPutBody) error { return s.applyJoinTokenPut(b, now) })
	case CmdJoinTokenConsume:
		return applyJSON(cmd.Body, func(b JoinTokenConsumeBody) error { return s.applyJoinTokenConsume(b, now) })
	case CmdJoinTokenRevoke:
		return applyJSON(cmd.Body, s.applyJoinTokenRevoke)
	case CmdMemberPut:
		return applyJSON(cmd.Body, s.applyMemberPut)
	case CmdMemberRemove:
		return applyJSON(cmd.Body, s.applyMemberRemove)
	case CmdCRLAdd:
		return applyJSON(cmd.Body, s.applyCRLAdd)
	case CmdGroupPut:
		return applyJSON(cmd.Body, s.applyGroupPut)
	case CmdGroupDelete:
		return applyJSON(cmd.Body, s.applyGroupDelete)
	case CmdGroupMemberAdd:
		return applyJSON(cmd.Body, s.applyGroupMemberAdd)
	case CmdGroupMemberRemove:
		return applyJSON(cmd.Body, s.applyGroupMemberRemove)
	case CmdAlertChannelPut:
		return applyJSON(cmd.Body, s.applyAlertChannelPut)
	case CmdAlertChannelDelete:
		return applyJSON(cmd.Body, s.applyAlertChannelDelete)
	case CmdAlertPolicyPut:
		return applyJSON(cmd.Body, s.applyAlertPolicyPut)
	default:
		return errcode.E(errcode.INVALID, "unknown command type")
	}
}

func applyJSON[T any](raw json.RawMessage, fn func(T) error) error {
	var body T
	if err := json.Unmarshal(raw, &body); err != nil {
		return errcode.E(errcode.INVALID, "invalid command body")
	}
	return fn(body)
}

func (s *State) applyBootstrap(b BootstrapBody) error {
	if s.ClusterID != "" {
		return errcode.E(errcode.CONFLICT, "cluster already bootstrapped")
	}
	if b.ClusterID == "" || b.AdminUser == "" || b.PasswordHash == "" || b.AdminUserID == "" {
		return errcode.E(errcode.INVALID, "bootstrap fields required")
	}
	s.ClusterID = b.ClusterID
	s.Policy.RBACCacheTTL = DefaultRBACCacheTTL
	for _, r := range builtinRoles() {
		s.Roles[r.ID] = r
	}
	s.Users[b.AdminUser] = User{
		ID:           b.AdminUserID,
		Username:     b.AdminUser,
		PasswordHash: b.PasswordHash,
		Status:       UserActive,
		CreatedUnix:  b.NowUnix,
	}
	s.UsersByID[b.AdminUserID] = b.AdminUser
	s.Bindings = append(s.Bindings, Binding{
		UserID:  b.AdminUserID,
		RoleID:  roleSuperAdmin,
		Scope:   ScopeCluster,
		ScopeID: "",
	})
	return nil
}

func (s *State) applyUserPut(b UserPutBody) error {
	if b.Username == "" || b.PasswordHash == "" {
		return errcode.E(errcode.INVALID, "username and password hash required")
	}
	if _, exists := s.Users[b.Username]; exists {
		return errcode.E(errcode.CONFLICT, "username already exists")
	}
	id := b.ID
	if id == "" {
		var err error
		id, err = newUUID()
		if err != nil {
			return err
		}
	}
	if existing, ok := s.UsersByID[id]; ok && existing != b.Username {
		return errcode.E(errcode.CONFLICT, "user id already exists")
	}
	s.Users[b.Username] = User{
		ID:           id,
		Username:     b.Username,
		PasswordHash: b.PasswordHash,
		DisplayName:  b.DisplayName,
		Email:        b.Email,
		Status:       UserActive,
	}
	s.UsersByID[id] = b.Username
	return nil
}

func (s *State) applyUserDisable(b UserDisableBody) error {
	u, ok := s.userByID(b.UserID)
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "user not found")
	}
	u.Status = UserDisabled
	s.Users[u.Username] = u
	return nil
}

func (s *State) applyLoginOK(b LoginOKBody, now time.Time) error {
	u, ok := s.Users[b.Username]
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "user not found")
	}
	u.FailCount = 0
	if u.Status == UserLocked && !now.Before(time.Unix(u.LockedUntilUnix, 0)) {
		u.Status = UserActive
		u.LockedUntilUnix = 0
	}
	u.LastLoginUnix = now.Unix()
	s.Users[b.Username] = u
	return nil
}

func (s *State) applyLoginFail(b LoginFailBody, now time.Time) error {
	u, ok := s.Users[b.Username]
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "user not found")
	}
	u.FailCount++
	if u.FailCount >= LockAfter && u.Status != UserDisabled {
		u.Status = UserLocked
		u.LockedUntilUnix = now.Add(LockFor).Unix()
	}
	s.Users[b.Username] = u
	return nil
}

func (s *State) applySessionPut(b SessionPutBody, now time.Time) error {
	if b.ID == "" {
		return errcode.E(errcode.INVALID, "session id required")
	}
	exp := b.ExpiresUnix
	if exp == 0 {
		exp = now.Add(SessionTTL).Unix()
	}
	s.Sessions[b.ID] = Session{
		ID:          b.ID,
		UserID:      b.UserID,
		CSRF:        b.CSRF,
		ExpiresUnix: exp,
	}
	return nil
}

func (s *State) applySessionDel(b SessionDelBody) error {
	delete(s.Sessions, b.ID)
	return nil
}

func (s *State) applyTokenPut(b TokenPutBody) error {
	if b.Hash == "" {
		return errcode.E(errcode.INVALID, "token hash required")
	}
	id := b.ID
	if id == "" {
		var err error
		id, err = newUUID()
		if err != nil {
			return err
		}
	}
	s.APITokens[id] = APIToken{
		ID:          id,
		UserID:      b.UserID,
		Name:        b.Name,
		Hash:        b.Hash,
		ExpiresUnix: b.ExpiresUnix,
	}
	return nil
}

func (s *State) applyTokenRevoke(b TokenRevokeBody) error {
	tok, ok := s.APITokens[b.ID]
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "api token not found")
	}
	tok.Revoked = true
	s.APITokens[b.ID] = tok
	return nil
}

func (s *State) applyRolePut(b RolePutBody) error {
	if b.ID == "" {
		return errcode.E(errcode.INVALID, "role id required")
	}
	s.Roles[b.ID] = Role{ID: b.ID, Name: b.Name, Perms: append([]string(nil), b.Perms...)}
	return nil
}

func (s *State) applyBindPut(b BindPutBody) error {
	if b.UserID == "" || b.RoleID == "" {
		return errcode.E(errcode.INVALID, "binding user and role required")
	}
	nb := Binding{UserID: b.UserID, RoleID: b.RoleID, ScopeID: b.ScopeID, Scope: b.Scope}
	for _, existing := range s.Bindings {
		if existing == nb {
			return nil
		}
	}
	s.Bindings = append(s.Bindings, nb)
	return nil
}

func (s *State) applyJoinTokenPut(b JoinTokenPutBody, now time.Time) error {
	if b.Hash == "" {
		return errcode.E(errcode.INVALID, "join token hash required")
	}
	id := b.ID
	if id == "" {
		var err error
		id, err = newUUID()
		if err != nil {
			return err
		}
	}
	ttl := time.Duration(b.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	s.JoinTokens[id] = JoinToken{
		ID:          id,
		Hash:        b.Hash,
		ExpiresUnix: now.Add(ttl).Unix(),
		Remaining:   b.Remaining,
	}
	return nil
}

func (s *State) applyJoinTokenConsume(b JoinTokenConsumeBody, now time.Time) error {
	want := hashToken(b.Plain)
	wantB := []byte(want)
	var rec *JoinToken
	var id string
	for k, tok := range s.JoinTokens {
		gotB := []byte(tok.Hash)
		if len(gotB) != len(wantB) {
			continue
		}
		if subtle.ConstantTimeCompare(gotB, wantB) == 1 {
			cp := tok
			rec = &cp
			id = k
			break
		}
	}
	if rec == nil {
		return errcode.E(errcode.INVALID, "invalid join token")
	}
	if rec.Revoked {
		return errcode.E(errcode.DENIED, "join token revoked")
	}
	if !now.Before(time.Unix(rec.ExpiresUnix, 0)) {
		return errcode.E(errcode.DENIED, "join token expired")
	}
	if rec.Remaining <= 0 {
		return errcode.E(errcode.DENIED, "join token exhausted")
	}
	rec.Remaining--
	s.JoinTokens[id] = *rec
	return nil
}

func (s *State) applyJoinTokenRevoke(b JoinTokenRevokeBody) error {
	tok, ok := s.JoinTokens[b.ID]
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "join token not found")
	}
	tok.Revoked = true
	s.JoinTokens[b.ID] = tok
	return nil
}

func (s *State) applyMemberPut(b MemberPutBody) error {
	if b.NodeID == "" {
		return errcode.E(errcode.INVALID, "node id required")
	}
	status := b.Status
	if status == "" {
		status = MemberAdmitted
	}
	s.Members[b.NodeID] = Member{
		NodeID:     b.NodeID,
		RaftAddr:   b.RaftAddr,
		CertSerial: strings.ToUpper(b.CertSerial),
		Status:     status,
	}
	return nil
}

func (s *State) applyMemberRemove(b MemberRemoveBody) error {
	m, ok := s.Members[b.NodeID]
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "member not found")
	}
	m.Status = MemberRevoked
	s.Members[b.NodeID] = m
	if m.CertSerial != "" {
		s.CRL[strings.ToUpper(m.CertSerial)] = struct{}{}
	}
	for id, g := range s.AgentGroups {
		var filtered []string
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
	return nil
}

func (s *State) applyCRLAdd(b CRLAddBody) error {
	if b.Serial == "" {
		return errcode.E(errcode.INVALID, "serial required")
	}
	s.CRL[strings.ToUpper(b.Serial)] = struct{}{}
	return nil
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

func validAlertChannelType(typ string) bool {
	switch typ {
	case "WEB", "WEBHOOK", "EMAIL", "WECOM", "DINGTALK", "SLACK":
		return true
	default:
		return false
	}
}

func normalizeAlertConfigJSON(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}", nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", errcode.E(errcode.INVALID, "config_json")
	}
	return raw, nil
}

func validAlertPercent(n int) bool {
	return n >= 1 && n <= 100
}

func (s *State) applyAlertChannelPut(b AlertChannelPutBody) error {
	name := strings.TrimSpace(b.Name)
	if b.ChannelID == "" || !agentGroupNameRE.MatchString(name) {
		return errcode.E(errcode.INVALID, "channel name")
	}
	if !validAlertChannelType(b.Type) {
		return errcode.E(errcode.INVALID, "channel type")
	}
	cfg, err := normalizeAlertConfigJSON(b.ConfigJSON)
	if err != nil {
		return err
	}
	cur := s.AlertChannels[b.ChannelID]
	if cur.ChannelID == "" {
		cur.ChannelID = b.ChannelID
		cur.CreatedUnix = b.NowUnix
	}
	cur.Type = b.Type
	cur.Name = name
	cur.Enabled = b.Enabled
	cur.ConfigJSON = cfg
	cur.UpdatedUnix = b.NowUnix
	s.AlertChannels[b.ChannelID] = cur
	return nil
}

func (s *State) applyAlertChannelDelete(b AlertChannelDeleteBody) error {
	if _, ok := s.AlertChannels[b.ChannelID]; !ok {
		return errcode.E(errcode.NOT_FOUND, "channel not found")
	}
	delete(s.AlertChannels, b.ChannelID)
	return nil
}

func (s *State) applyAlertPolicyPut(b AlertPolicyPutBody) error {
	if b.DedupWindowSec < 1 {
		return errcode.E(errcode.INVALID, "dedup_window_sec")
	}
	if !validAlertPercent(b.CPUHighPercent) || !validAlertPercent(b.MemoryHighPercent) || !validAlertPercent(b.DiskHighPercent) {
		return errcode.E(errcode.INVALID, "threshold percent")
	}
	if b.HighConsecutiveMins < 1 || b.HighConsecutiveMins > 60 {
		return errcode.E(errcode.INVALID, "high_consecutive_mins")
	}
	if b.SuspectTooLongSec < 1 || b.SuspectTooLongSec > 86400 {
		return errcode.E(errcode.INVALID, "suspect_too_long_sec")
	}
	s.AlertPolicy = AlertPolicy{
		DedupWindowSec:      b.DedupWindowSec,
		NotifyOnResolve:     b.NotifyOnResolve,
		CPUHighPercent:      b.CPUHighPercent,
		MemoryHighPercent:   b.MemoryHighPercent,
		DiskHighPercent:     b.DiskHighPercent,
		HighConsecutiveMins: b.HighConsecutiveMins,
		SuspectTooLongSec:   b.SuspectTooLongSec,
	}
	return nil
}

func (s *State) applyGroupMemberRemove(b GroupMemberBody) error {
	g, ok := s.AgentGroups[b.GroupID]
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "group not found")
	}
	out := make([]string, 0, len(g.MemberIDs))
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

func (s *State) userByID(id string) (User, bool) {
	uname, ok := s.UsersByID[id]
	if !ok {
		return User{}, false
	}
	u, ok := s.Users[uname]
	return u, ok
}

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

func roleHasPerm(role Role, perm string) bool {
	for _, p := range role.Perms {
		if p == perm {
			return true
		}
	}
	return false
}

func (s *State) SessionByID(id string) (Session, bool) {
	sess, ok := s.Sessions[id]
	return sess, ok
}

func (s *State) TokenByPlain(plain string) (APIToken, bool) {
	want := hashToken(plain)
	wantB := []byte(want)
	for _, tok := range s.APITokens {
		gotB := []byte(tok.Hash)
		if len(gotB) != len(wantB) {
			continue
		}
		if subtle.ConstantTimeCompare(gotB, wantB) == 1 {
			return tok, true
		}
	}
	return APIToken{}, false
}

func (s *State) JoinTokenByPlain(plain string) (JoinToken, bool) {
	want := hashToken(plain)
	wantB := []byte(want)
	for _, tok := range s.JoinTokens {
		gotB := []byte(tok.Hash)
		if len(gotB) != len(wantB) {
			continue
		}
		if subtle.ConstantTimeCompare(gotB, wantB) == 1 {
			return tok, true
		}
	}
	return JoinToken{}, false
}

func (s *State) Member(nodeID string) (Member, bool) {
	m, ok := s.Members[nodeID]
	return m, ok
}

// FSM is the in-memory Raft finite-state machine.
type FSM struct {
	mu    sync.Mutex
	state *State
}

func NewFSM() *FSM {
	return &FSM{state: NewState()}
}

var _ raft.FSM = (*FSM)(nil)

func (f *FSM) Apply(l *raft.Log) any {
	var cmd Command
	if err := json.Unmarshal(l.Data, &cmd); err != nil {
		return errcode.E(errcode.INVALID, "invalid command")
	}
	now := l.AppendedAt
	if now.IsZero() {
		now = time.Now()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.state.Apply(cmd, now); err != nil {
		return err
	}
	return nil
}

func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := json.Marshal(f.state)
	if err != nil {
		return nil, err
	}
	return &fsmSnapshot{data: data}, nil
}

func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	s := NewState()
	if err := json.Unmarshal(data, s); err != nil {
		return err
	}
	s.ensure()
	f.mu.Lock()
	f.state = s
	f.mu.Unlock()
	return nil
}

func (f *FSM) View() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneState(f.state)
}

func cloneState(s *State) State {
	data, err := json.Marshal(s)
	if err != nil {
		out := NewState()
		return *out
	}
	out := NewState()
	if err := json.Unmarshal(data, out); err != nil {
		return *NewState()
	}
	out.ensure()
	return *out
}

type fsmSnapshot struct {
	data []byte
}

func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s.data); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *fsmSnapshot) Release() {}

const (
	roleSuperAdmin   = "super_admin"
	roleClusterAdmin = "cluster_admin"
	roleOperator     = "operator"
	roleViewer       = "viewer"
)

func builtinRoles() []Role {
	return []Role{
		{ID: roleSuperAdmin, Name: "Super Admin", Perms: append([]string(nil), allPermissions...)},
		{ID: roleClusterAdmin, Name: "Cluster Admin", Perms: clusterAdminPermissions()},
		{ID: roleOperator, Name: "Operator", Perms: append([]string(nil), operatorPermissions...)},
		{ID: roleViewer, Name: "Viewer", Perms: append([]string(nil), viewerPermissions...)},
	}
}

func clusterAdminPermissions() []string {
	out := make([]string, 0, len(allPermissions))
	for _, p := range allPermissions {
		switch p {
		case "user.delete", "role.manage", "command.execute", "command.execute.batch":
			continue
		default:
			out = append(out, p)
		}
	}
	return out
}

var allPermissions = []string{
	"cluster.read", "cluster.manage",
	"node.read", "node.manage", "node.remove",
	"process.read", "process.create", "process.update", "process.delete",
	"process.start", "process.stop", "process.restart",
	"process.config.read", "process.config.update",
	"process.logs.read", "process.logs.download",
	"user.read", "user.create", "user.update", "user.delete",
	"role.read", "role.manage",
	"audit.read",
	"batch.execute", "alert.read", "alert.manage", "backup.read", "backup.manage",
	"command.execute", "command.execute.batch",
}

var operatorPermissions = []string{
	"cluster.read", "node.read", "process.read",
	"process.start", "process.stop", "process.restart",
	"process.config.read", "process.logs.read",
	"batch.execute", "alert.read",
}

var viewerPermissions = []string{
	"cluster.read", "node.read", "process.read",
	"process.config.read", "process.logs.read",
	"alert.read",
}
