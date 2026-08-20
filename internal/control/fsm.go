package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/raft"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/schedule"
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

type BackupPolicy struct {
	PolicyID           string   `json:"policy_id,omitempty"`
	Name               string   `json:"name,omitempty"`
	Enabled            bool     `json:"enabled"`
	ScheduleCron       string   `json:"schedule_cron,omitempty"`
	Timezone           string   `json:"timezone,omitempty"`
	TargetSelector     string   `json:"target_selector,omitempty"`
	TargetIDs          []string `json:"target_ids,omitempty"`
	Sink               string   `json:"sink,omitempty"`
	DestinationProfile string   `json:"destination_profile,omitempty"`
	RetentionKeepLast  int      `json:"retention_keep_last,omitempty"`
	RetentionKeepDays  int      `json:"retention_keep_days,omitempty"`
	RetentionMaxBytes  int64    `json:"retention_max_bytes,omitempty"`
	TimeoutSeconds     int      `json:"timeout_seconds,omitempty"`
	MaxConcurrency     int      `json:"max_concurrency,omitempty"`
	UnavailablePolicy  string   `json:"unavailable_policy,omitempty"`
	Revision           int64    `json:"revision,omitempty"`
}

type ReplicationPolicy struct {
	PolicyID            string             `json:"policy_id,omitempty"`
	Name                string             `json:"name,omitempty"`
	Enabled             bool               `json:"enabled"`
	SourceSelector      string             `json:"source_selector,omitempty"`
	SourceIDs           []string           `json:"source_ids,omitempty"`
	ReplicaFactor       int                `json:"replica_factor,omitempty"`
	Routes              []ReplicationRoute `json:"routes,omitempty"`
	Trigger             string             `json:"trigger,omitempty"`
	PrimaryPolicyIDs    []string           `json:"primary_policy_ids,omitempty"`
	ScheduleCron        string             `json:"schedule_cron,omitempty"`
	Timezone            string             `json:"timezone,omitempty"`
	RetentionKeepLast   int                `json:"retention_keep_last,omitempty"`
	RetentionKeepDays   int                `json:"retention_keep_days,omitempty"`
	RetentionMaxBytes   int64              `json:"retention_max_bytes,omitempty"`
	MaxConcurrency      int                `json:"max_concurrency,omitempty"`
	VerifyAfterCopy     bool               `json:"verify_after_copy"`
	BandwidthLimit      int64              `json:"bandwidth_limit,omitempty"`
	TopologyConstraints map[string]string  `json:"topology_constraints,omitempty"`
	Revision            int64              `json:"revision,omitempty"`
}

type FireRecord struct {
	FireKey        string `json:"fire_key"`
	PolicyID       string `json:"policy_id"`
	RunID          string `json:"run_id"`
	ScheduledUnix  int64  `json:"scheduled_unix"`
	ClaimedUnix    int64  `json:"claimed_unix"`
	LeaseUntilUnix int64  `json:"lease_until_unix"`
	LeaderTerm     uint64 `json:"leader_term"`
	Status         string `json:"status"`
}

type ClusterBackupRun struct {
	RunID              string   `json:"run_id"`
	PolicyID           string   `json:"policy_id"`
	PolicyRevision     int64    `json:"policy_revision"`
	TargetNodeIDs      []string `json:"target_node_ids"`
	Status             string   `json:"status"`
	Success            int      `json:"success"`
	Failed             int      `json:"failed"`
	Unavailable        int      `json:"unavailable"`
	Timeout            int      `json:"timeout"`
	CreatedUnix        int64    `json:"created_unix"`
	StartedUnix        int64    `json:"started_unix"`
	FinishedUnix       int64    `json:"finished_unix"`
	Sink               string   `json:"sink,omitempty"`
	DestinationProfile string   `json:"destination_profile,omitempty"`
	MaxConcurrency     int      `json:"max_concurrency,omitempty"`
	TimeoutSeconds     int      `json:"timeout_seconds,omitempty"`
	UnavailablePolicy  string   `json:"unavailable_policy,omitempty"`
	LeaseUntilUnix     int64    `json:"lease_until_unix,omitempty"`
}

type ClusterBackupTask struct {
	RunID        string `json:"run_id"`
	TaskID       string `json:"task_id"`
	NodeID       string `json:"node_id"`
	SourceNodeID string `json:"source_node_id,omitempty"`
	SnapshotID   string `json:"snapshot_id"`
	SHA256       string `json:"sha256"`
	Status       string `json:"status"`
	Bytes        int64  `json:"bytes"`
	ErrorCode    string `json:"error_code"`
	ErrorSummary string `json:"error_summary"`
	LeaderTerm   uint64 `json:"leader_term"`
	UpdatedUnix  int64  `json:"updated_unix"`
}

type Policy struct {
	RBACCacheTTL time.Duration
}

type State struct {
	ClusterID           string                       `json:"cluster_id"`
	Users               map[string]User              `json:"users"`       // by username
	UsersByID           map[string]string            `json:"users_by_id"` // id → username
	Roles               map[string]Role              `json:"roles"`
	Bindings            []Binding                    `json:"bindings"`
	Sessions            map[string]Session           `json:"sessions"`
	APITokens           map[string]APIToken          `json:"api_tokens"`
	JoinTokens          map[string]JoinToken         `json:"join_tokens"`    // by id
	Members             map[string]Member            `json:"members"`        // by node_id
	CRL                 map[string]struct{}          `json:"crl"`            // cert serial hex
	AgentGroups         map[string]AgentGroup        `json:"agent_groups"`   // by group_id
	AlertChannels       map[string]AlertChannel      `json:"alert_channels"` // by channel_id
	AlertPolicy         AlertPolicy                  `json:"alert_policy"`
	BackupPolicies      map[string]BackupPolicy      `json:"backup_policies"`
	ReplicationPolicies map[string]ReplicationPolicy `json:"replication_policies"`
	BackupFireLedger    map[string]FireRecord        `json:"backup_fire_ledger"`
	BackupRuns          map[string]ClusterBackupRun  `json:"backup_runs"`
	BackupTasks         map[string]ClusterBackupTask `json:"backup_tasks"`
	ReplicationRuns     map[string]ClusterBackupRun  `json:"replication_runs"`
	ReplicationTasks    map[string]ClusterBackupTask `json:"replication_tasks"`
	BackupRunTerms      map[string]uint64            `json:"backup_run_terms"`
	ReplicationRunTerms map[string]uint64            `json:"replication_run_terms"`
	Policy              Policy                       `json:"policy"`
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
	if s.BackupPolicies == nil {
		s.BackupPolicies = map[string]BackupPolicy{}
	}
	if s.ReplicationPolicies == nil {
		s.ReplicationPolicies = map[string]ReplicationPolicy{}
	}
	if s.BackupFireLedger == nil {
		s.BackupFireLedger = map[string]FireRecord{}
	}
	if s.BackupRuns == nil {
		s.BackupRuns = map[string]ClusterBackupRun{}
	}
	if s.BackupTasks == nil {
		s.BackupTasks = map[string]ClusterBackupTask{}
	}
	if s.ReplicationRuns == nil {
		s.ReplicationRuns = map[string]ClusterBackupRun{}
	}
	if s.ReplicationTasks == nil {
		s.ReplicationTasks = map[string]ClusterBackupTask{}
	}
	if s.BackupRunTerms == nil {
		s.BackupRunTerms = map[string]uint64{}
	}
	if s.ReplicationRunTerms == nil {
		s.ReplicationRunTerms = map[string]uint64{}
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
	case CmdBackupPolicyPut:
		return applyJSON(cmd.Body, s.applyBackupPolicyPut)
	case CmdBackupPolicyDelete:
		return applyJSON(cmd.Body, s.applyBackupPolicyDelete)
	case CmdReplicationPolicyPut:
		return applyJSON(cmd.Body, s.applyReplicationPolicyPut)
	case CmdReplicationPolicyDelete:
		return applyJSON(cmd.Body, s.applyReplicationPolicyDelete)
	case CmdBackupFireClaim:
		_, _, err := s.ClaimFireBody(cmd.Body, now)
		return err
	case CmdBackupScheduledRunClaim:
		return applyJSON(cmd.Body, func(b ScheduledRunClaimBody) error {
			_, _, _, err := s.ClaimScheduledRun(b, now)
			return err
		})
	case CmdBackupRunCreate:
		return applyJSON(cmd.Body, s.applyCreateRun)
	case CmdBackupRunClaim:
		return applyJSON(cmd.Body, func(b RunClaimBody) error {
			_, err := s.ClaimRun(b)
			return err
		})
	case CmdBackupTaskUpdate:
		return applyJSON(cmd.Body, s.applyUpdateTask)
	case CmdBackupRetryFailedTasks:
		return applyJSON(cmd.Body, s.applyRetryFailedTasks)
	case CmdBackupRunFinish:
		return applyJSON(cmd.Body, s.applyFinishRun)
	case CmdRunMetadataPrune:
		return applyJSON(cmd.Body, func(b PruneRunMetadataBody) error { return s.applyPruneRunMetadata(b) })
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

func validPolicySelector(selector string) bool {
	switch selector {
	case "ALL_ADMITTED", "AGENT_GROUP", "EXPLICIT_NODES":
		return true
	default:
		return false
	}
}

func validatePolicySchedule(cronExpr, timezone string) error {
	if err := schedule.ValidateCron(cronExpr); err != nil {
		return err
	}
	return schedule.ValidateTimezone(timezone)
}

func validateTargetIDs(selector string, ids []string) error {
	if !validPolicySelector(selector) {
		return errcode.E(errcode.INVALID, "target selector")
	}
	if selector == "AGENT_GROUP" || selector == "EXPLICIT_NODES" {
		if len(ids) == 0 {
			return errcode.E(errcode.INVALID, "target ids required")
		}
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || trimmed != id {
			return errcode.E(errcode.INVALID, "target id")
		}
		if _, ok := seen[trimmed]; ok {
			return errcode.E(errcode.INVALID, "duplicate target")
		}
		seen[trimmed] = struct{}{}
	}
	return nil
}

func (s *State) validateExplicitTargets(selector string, ids []string) error {
	if selector != "EXPLICIT_NODES" {
		return nil
	}
	for _, id := range ids {
		member, ok := s.Members[id]
		if !ok || member.Status != MemberAdmitted {
			return errcode.E(errcode.INVALID, "target node not admitted")
		}
	}
	return nil
}

func (s *State) validateAgentGroups(selector string, ids []string) error {
	if selector != "AGENT_GROUP" {
		return nil
	}
	for _, id := range ids {
		if _, ok := s.AgentGroups[id]; !ok {
			return errcode.E(errcode.INVALID, "agent group not found")
		}
	}
	return nil
}

func validUnavailablePolicy(policy string) bool {
	return policy == "RECORD_AND_CONTINUE" || policy == "FAIL_FAST"
}

func (s *State) applyBackupPolicyPut(b BackupPolicyPutBody) error {
	if err := requireOperationID(b.OperationID); err != nil {
		return err
	}
	name := strings.TrimSpace(b.Name)
	if b.PolicyID == "" || name == "" {
		return errcode.E(errcode.INVALID, "policy id and name required")
	}
	for id, policy := range s.BackupPolicies {
		if id != b.PolicyID && policy.Name == name {
			return errcode.E(errcode.CONFLICT, "backup policy name already exists")
		}
	}
	if err := validatePolicySchedule(b.ScheduleCron, b.Timezone); err != nil {
		return err
	}
	if err := validateTargetIDs(b.TargetSelector, b.TargetIDs); err != nil {
		return err
	}
	if err := s.validateExplicitTargets(b.TargetSelector, b.TargetIDs); err != nil {
		return err
	}
	if err := s.validateAgentGroups(b.TargetSelector, b.TargetIDs); err != nil {
		return err
	}
	if b.Sink != "fs" && b.Sink != "s3" {
		return errcode.E(errcode.INVALID, "sink")
	}
	if b.Sink == "s3" && strings.TrimSpace(b.DestinationProfile) == "" {
		return errcode.E(errcode.INVALID, "destination profile required")
	}
	if b.RetentionKeepLast < 0 || b.RetentionKeepDays < 0 || b.RetentionMaxBytes < 0 || b.TimeoutSeconds < 0 || b.MaxConcurrency < 0 {
		return errcode.E(errcode.INVALID, "invalid retention or limits")
	}
	unavailablePolicy := b.UnavailablePolicy
	if unavailablePolicy == "" {
		unavailablePolicy = "RECORD_AND_CONTINUE"
	}
	if !validUnavailablePolicy(unavailablePolicy) {
		return errcode.E(errcode.INVALID, "unavailable policy")
	}
	cur := s.BackupPolicies[b.PolicyID]
	cur.PolicyID, cur.Name, cur.Enabled = b.PolicyID, name, b.Enabled
	cur.ScheduleCron, cur.Timezone = strings.TrimSpace(b.ScheduleCron), strings.TrimSpace(b.Timezone)
	cur.TargetSelector, cur.TargetIDs = b.TargetSelector, append([]string(nil), b.TargetIDs...)
	cur.Sink, cur.DestinationProfile = b.Sink, strings.TrimSpace(b.DestinationProfile)
	cur.RetentionKeepLast, cur.RetentionKeepDays, cur.RetentionMaxBytes = b.RetentionKeepLast, b.RetentionKeepDays, b.RetentionMaxBytes
	cur.TimeoutSeconds, cur.MaxConcurrency, cur.UnavailablePolicy = b.TimeoutSeconds, b.MaxConcurrency, unavailablePolicy
	cur.Revision++
	s.BackupPolicies[b.PolicyID] = cur
	return nil
}

func (s *State) applyBackupPolicyDelete(b BackupPolicyDeleteBody) error {
	if err := requireOperationID(b.OperationID); err != nil {
		return err
	}
	if _, ok := s.BackupPolicies[b.PolicyID]; !ok {
		return errcode.E(errcode.NOT_FOUND, "backup policy not found")
	}
	delete(s.BackupPolicies, b.PolicyID)
	return nil
}

const defaultFireLease = 2 * time.Minute

const (
	maxMetadataIDLen   = 128
	maxFireKeyLen      = 256
	maxErrorCodeLen    = 128
	maxErrorSummaryLen = 2048
	maxSnapshotIDLen   = 512
	maxSHA256Len       = 128
	maxTargetNodeIDs   = 1024
)

func requireOperationID(operationID string) error {
	if strings.TrimSpace(operationID) == "" {
		return errcode.E(errcode.INVALID, "operation_id required")
	}
	return nil
}

func runIDForFire(fireKey string) string {
	sum := sha256.Sum256([]byte(fireKey))
	return "run-" + fmt.Sprintf("%x", sum[:12])
}

// ClaimFire creates exactly one durable run identifier per scheduled fire.
// A live claim is returned unchanged; an expired lease may be taken by a newer term.
func (s *State) ClaimFire(b FireClaimBody, now time.Time) (FireRecord, bool, error) {
	s.ensure()
	if err := requireOperationID(b.OperationID); err != nil {
		return FireRecord{}, false, err
	}
	if !validMetadataString(b.FireKey, maxFireKeyLen) || !validMetadataString(b.PolicyID, maxMetadataIDLen) || b.LeaderTerm == 0 {
		return FireRecord{}, false, errcode.E(errcode.INVALID, "fire key, policy id, and leader term required")
	}
	if b.LeaseUntilUnix != 0 && (b.LeaseUntilUnix <= now.Unix() || b.LeaseUntilUnix > now.Add(24*time.Hour).Unix()) {
		return FireRecord{}, false, errcode.E(errcode.INVALID, "invalid fire lease")
	}
	if current, ok := s.BackupFireLedger[b.FireKey]; ok {
		if current.PolicyID != b.PolicyID {
			return FireRecord{}, false, errcode.E(errcode.CONFLICT, "fire key belongs to another policy")
		}
		if current.LeaseUntilUnix > now.Unix() || b.LeaderTerm <= current.LeaderTerm {
			return current, false, nil
		}
		current.LeaderTerm = b.LeaderTerm
		current.ClaimedUnix = now.Unix()
		current.LeaseUntilUnix = b.LeaseUntilUnix
		if current.LeaseUntilUnix == 0 {
			current.LeaseUntilUnix = now.Add(defaultFireLease).Unix()
		}
		current.Status = "CLAIMED"
		s.BackupFireLedger[b.FireKey] = current
		return current, true, nil
	}
	leaseUntil := b.LeaseUntilUnix
	if leaseUntil == 0 {
		leaseUntil = now.Add(defaultFireLease).Unix()
	}
	if leaseUntil <= now.Unix() || leaseUntil > now.Add(24*time.Hour).Unix() {
		return FireRecord{}, false, errcode.E(errcode.INVALID, "invalid fire lease")
	}
	scheduled := b.ScheduledUnix
	if scheduled == 0 {
		scheduled = now.Unix()
	}
	record := FireRecord{FireKey: b.FireKey, PolicyID: b.PolicyID, RunID: runIDForFire(b.FireKey), ScheduledUnix: scheduled, ClaimedUnix: now.Unix(), LeaseUntilUnix: leaseUntil, LeaderTerm: b.LeaderTerm, Status: "CLAIMED"}
	s.BackupFireLedger[b.FireKey] = record
	return record, true, nil
}

// ClaimFireBody supports the command dispatcher while direct callers receive the claim result.
func (s *State) ClaimFireBody(raw json.RawMessage, now time.Time) (FireRecord, bool, error) {
	var b FireClaimBody
	if err := json.Unmarshal(raw, &b); err != nil {
		return FireRecord{}, false, errcode.E(errcode.INVALID, "invalid command body")
	}
	return s.ClaimFire(b, now)
}

// ClaimScheduledRun commits the fire ledger entry and the frozen run as one
// FSM transition. acquired is true for a first claim or a lease takeover, and
// false while another leader holds a live lease.
func (s *State) ClaimScheduledRun(b ScheduledRunClaimBody, now time.Time) (FireRecord, ClusterBackupRun, bool, error) {
	s.ensure()
	current, exists := s.BackupFireLedger[b.Fire.FireKey]
	if exists {
		run, ok := s.BackupRuns[current.RunID]
		if !ok {
			return FireRecord{}, ClusterBackupRun{}, false, errcode.E(errcode.CONFLICT, "scheduled fire has no run")
		}
		if current.PolicyID != b.Fire.PolicyID {
			return FireRecord{}, ClusterBackupRun{}, false, errcode.E(errcode.CONFLICT, "fire key belongs to another policy")
		}
		if terminalRunStatus(run.Status) {
			return current, run, false, nil
		}
		if current.LeaseUntilUnix > now.Unix() || b.Fire.LeaderTerm <= current.LeaderTerm {
			return current, run, false, nil
		}
		record, acquired, err := s.ClaimFire(b.Fire, now)
		if err == nil && acquired {
			run.LeaseUntilUnix = record.LeaseUntilUnix
			s.BackupRuns[run.RunID] = run
			s.BackupRunTerms[run.RunID] = record.LeaderTerm
		}
		return record, run, acquired, err
	}
	if b.Run.RunID != runIDForFire(b.Fire.FireKey) || b.Run.PolicyID != b.Fire.PolicyID {
		return FireRecord{}, ClusterBackupRun{}, false, errcode.E(errcode.INVALID, "scheduled run does not match fire")
	}
	// Validate the run before creating the ledger record, so an invalid policy
	// cannot leave an orphaned durable fire.
	if err := s.validateCreateRun(b.Run, b.Fire.LeaderTerm, false); err != nil {
		return FireRecord{}, ClusterBackupRun{}, false, err
	}
	record, acquired, err := s.ClaimFire(b.Fire, now)
	if err != nil || !acquired {
		return record, ClusterBackupRun{}, acquired, err
	}
	b.Run.TargetNodeIDs = append([]string(nil), b.Run.TargetNodeIDs...)
	if b.Run.LeaseUntilUnix == 0 {
		b.Run.LeaseUntilUnix = record.LeaseUntilUnix
	}
	s.BackupRuns[b.Run.RunID] = b.Run
	s.BackupRunTerms[b.Run.RunID] = b.Fire.LeaderTerm
	return record, b.Run, true, nil
}

func (s *State) validateCreateRun(run ClusterBackupRun, term uint64, replication bool) error {
	clone := cloneState(s)
	return clone.applyCreateRun(CreateRunBody{OperationID: "scheduled-validate", LeaderTerm: term, Run: run, Replication: replication})
}

func runMaps(s *State, replication bool) (map[string]ClusterBackupRun, map[string]ClusterBackupTask) {
	if replication {
		return s.ReplicationRuns, s.ReplicationTasks
	}
	return s.BackupRuns, s.BackupTasks
}

func runTerms(s *State, replication bool) map[string]uint64 {
	if replication {
		return s.ReplicationRunTerms
	}
	return s.BackupRunTerms
}

func terminalRunStatus(status string) bool {
	return status == "SUCCEEDED" || status == "SUCCESS" || status == "PARTIAL" || status == "FAILED" || status == "CANCELED"
}

func terminalTaskStatus(status string) bool {
	return status == "SUCCEEDED" || status == "SUCCESS" || status == "FAILED" || status == "TIMEOUT" || status == "UNAVAILABLE" || status == "CONFIG_MISSING" || status == "RETENTION_FAILED" || status == "SKIPPED"
}

func validRunStatus(status string) bool {
	return status == "PENDING" || status == "RUNNING" || terminalRunStatus(status)
}

func validTaskStatus(status string) bool {
	return status == "PENDING" || status == "RUNNING" || terminalTaskStatus(status)
}

func validMetadataString(value string, max int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= max
}

func validateTargetNodeIDs(ids []string) error {
	if len(ids) > maxTargetNodeIDs {
		return errcode.E(errcode.INVALID, "too many target nodes")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !validMetadataString(id, maxMetadataIDLen) {
			return errcode.E(errcode.INVALID, "invalid target node id")
		}
		if _, ok := seen[id]; ok {
			return errcode.E(errcode.INVALID, "duplicate target node id")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *State) validateRunTargets(selector string, selectorIDs, targets []string) error {
	if err := validateTargetNodeIDs(targets); err != nil {
		return err
	}
	switch selector {
	case "EXPLICIT_NODES":
		if !equalStrings(targets, selectorIDs) {
			return errcode.E(errcode.CONFLICT, "target nodes changed")
		}
	case "ALL_ADMITTED":
		admitted := make([]string, 0, len(s.Members))
		for id, member := range s.Members {
			if member.Status == MemberAdmitted {
				admitted = append(admitted, id)
			}
		}
		sort.Strings(admitted)
		got := append([]string(nil), targets...)
		sort.Strings(got)
		if !equalStrings(got, admitted) {
			return errcode.E(errcode.CONFLICT, "target nodes changed")
		}
	case "AGENT_GROUP":
		allowed := map[string]struct{}{}
		for _, groupID := range selectorIDs {
			group, ok := s.AgentGroups[groupID]
			if !ok {
				return errcode.E(errcode.INVALID, "agent group not found")
			}
			for _, nodeID := range group.MemberIDs {
				if s.isAdmittedMember(nodeID) {
					allowed[nodeID] = struct{}{}
				}
			}
		}
		got := make([]string, 0, len(targets))
		for _, nodeID := range targets {
			if _, ok := allowed[nodeID]; !ok {
				return errcode.E(errcode.INVALID, "target node not in agent group")
			}
			got = append(got, nodeID)
		}
		want := make([]string, 0, len(allowed))
		for nodeID := range allowed {
			want = append(want, nodeID)
		}
		sort.Strings(got)
		sort.Strings(want)
		if !equalStrings(got, want) {
			return errcode.E(errcode.CONFLICT, "target nodes changed")
		}
	case "":
		// Legacy hand-built test/state records may omit a selector; keep their
		// metadata readable while policy-created runs use the strict selectors.
		return nil
	default:
		return errcode.E(errcode.INVALID, "target selector")
	}
	return nil
}

func validateTaskMetadata(task ClusterBackupTask) error {
	if !validMetadataString(task.RunID, maxMetadataIDLen) || !validMetadataString(task.TaskID, maxMetadataIDLen) || !validMetadataString(task.NodeID, maxMetadataIDLen) {
		return errcode.E(errcode.INVALID, "invalid task id")
	}
	if task.SourceNodeID != "" && !validMetadataString(task.SourceNodeID, maxMetadataIDLen) {
		return errcode.E(errcode.INVALID, "invalid source node id")
	}
	if len(task.SnapshotID) > maxSnapshotIDLen || len(task.SHA256) > maxSHA256Len || len(task.ErrorCode) > maxErrorCodeLen || len(task.ErrorSummary) > maxErrorSummaryLen {
		return errcode.E(errcode.INVALID, "task metadata too long")
	}
	if !validTaskStatus(task.Status) {
		return errcode.E(errcode.INVALID, "invalid task status")
	}
	return nil
}

func (s *State) CreateRun(b CreateRunBody) error { return s.applyCreateRun(b) }

func (s *State) applyCreateRun(b CreateRunBody) error {
	s.ensure()
	if err := requireOperationID(b.OperationID); err != nil {
		return err
	}
	if b.LeaderTerm == 0 || !validMetadataString(b.Run.RunID, maxMetadataIDLen) || !validMetadataString(b.Run.PolicyID, maxMetadataIDLen) || !validRunStatus(b.Run.Status) {
		return errcode.E(errcode.INVALID, "run id, status, and leader term required")
	}
	if err := validateTargetNodeIDs(b.Run.TargetNodeIDs); err != nil {
		return err
	}
	var policyRevision int64
	var replicationPolicy ReplicationPolicy
	if b.Replication {
		policy, ok := s.ReplicationPolicies[b.Run.PolicyID]
		if !ok {
			return errcode.E(errcode.NOT_FOUND, "replication policy not found")
		}
		replicationPolicy = policy
		policyRevision = policy.Revision
		if policy.SourceSelector != "" {
			if err := s.validateRunTargets(policy.SourceSelector, policy.SourceIDs, b.Run.TargetNodeIDs); err != nil {
				return err
			}
		}
	} else {
		policy, ok := s.BackupPolicies[b.Run.PolicyID]
		if !ok {
			return errcode.E(errcode.NOT_FOUND, "backup policy not found")
		}
		policyRevision = policy.Revision
		if policy.TargetSelector != "" {
			if err := s.validateRunTargets(policy.TargetSelector, policy.TargetIDs, b.Run.TargetNodeIDs); err != nil {
				return err
			}
		}
	}
	if b.Run.PolicyRevision != policyRevision {
		return errcode.E(errcode.CONFLICT, "policy revision changed")
	}
	if !b.Replication && len(b.Tasks) > 0 {
		return errcode.E(errcode.INVALID, "initial tasks require replication run")
	}
	if b.Replication && len(b.Tasks) > 0 {
		if err := validateInitialReplicationTasks(b.Run, b.Tasks, replicationPolicy); err != nil {
			return err
		}
	}
	runs, tasks := runMaps(s, b.Replication)
	terms := runTerms(s, b.Replication)
	if current, ok := runs[b.Run.RunID]; ok {
		if (!b.Replication && reflect.DeepEqual(current, b.Run)) || (b.Replication && sameFrozenRun(current, b.Run) && sameInitialRouteTasks(tasks, b.Run.RunID, b.Tasks)) {
			return nil
		}
		return errcode.E(errcode.CONFLICT, "run already exists")
	}
	b.Run.TargetNodeIDs = append([]string(nil), b.Run.TargetNodeIDs...)
	runs[b.Run.RunID] = b.Run
	terms[b.Run.RunID] = b.LeaderTerm
	for _, task := range b.Tasks {
		task.LeaderTerm = b.LeaderTerm
		tasks[taskMapKey(task)] = task
	}
	return nil
}

func validateInitialReplicationTasks(run ClusterBackupRun, tasks []ClusterBackupTask, policy ReplicationPolicy) error {
	if len(tasks) > maxTargetNodeIDs {
		return errcode.E(errcode.INVALID, "too many initial tasks")
	}
	expected := make(map[string]struct{})
	for _, route := range policy.Routes {
		for _, targetID := range route.TargetNodeIDs {
			expected[route.SourceNodeID+"\x00"+targetID] = struct{}{}
		}
	}
	if len(tasks) != len(expected) {
		return errcode.E(errcode.INVALID, "initial tasks do not match frozen routes")
	}
	seenTaskIDs := make(map[string]struct{}, len(tasks))
	seenRoutes := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if err := validateTaskMetadata(task); err != nil {
			return err
		}
		if task.RunID != run.RunID || task.SourceNodeID == "" || task.Status != "PENDING" || (task.SnapshotID == "") != (task.SHA256 == "") || task.Bytes != 0 || task.ErrorCode != "" || task.ErrorSummary != "" {
			return errcode.E(errcode.INVALID, "invalid initial replication task")
		}
		if _, ok := seenTaskIDs[task.TaskID]; ok {
			return errcode.E(errcode.INVALID, "duplicate initial task id")
		}
		seenTaskIDs[task.TaskID] = struct{}{}
		routeKey := task.SourceNodeID + "\x00" + task.NodeID
		if _, ok := expected[routeKey]; !ok {
			return errcode.E(errcode.INVALID, "initial task route not in policy")
		}
		if _, ok := seenRoutes[routeKey]; ok {
			return errcode.E(errcode.INVALID, "duplicate initial task route")
		}
		seenRoutes[routeKey] = struct{}{}
	}
	return nil
}

func sameFrozenRun(current, requested ClusterBackupRun) bool {
	return current.RunID == requested.RunID &&
		current.PolicyID == requested.PolicyID &&
		current.PolicyRevision == requested.PolicyRevision &&
		equalStrings(current.TargetNodeIDs, requested.TargetNodeIDs) &&
		current.MaxConcurrency == requested.MaxConcurrency
}

func sameInitialRouteTasks(tasks map[string]ClusterBackupTask, runID string, requested []ClusterBackupTask) bool {
	if len(requested) == 0 {
		return true
	}
	count := 0
	for _, task := range tasks {
		if task.RunID == runID {
			count++
		}
	}
	if count != len(requested) {
		return false
	}
	for _, task := range requested {
		current, ok := tasks[taskMapKey(task)]
		if !ok || current.SourceNodeID != task.SourceNodeID || current.NodeID != task.NodeID {
			return false
		}
	}
	return true
}

// ClaimRun moves an expired running run to a newer leader term and renews its
// lease. The boolean is false for a live lease, a terminal run, or a term that
// is not newer than the current owner.
func (s *State) ClaimRun(b RunClaimBody) (bool, error) {
	s.ensure()
	if err := requireOperationID(b.OperationID); err != nil {
		return false, err
	}
	if !validMetadataString(b.RunID, maxMetadataIDLen) || b.LeaderTerm == 0 || b.UpdatedUnix <= 0 || b.LeaseUntilUnix <= b.UpdatedUnix || b.LeaseUntilUnix > b.UpdatedUnix+int64((24*time.Hour)/time.Second) {
		return false, errcode.E(errcode.INVALID, "invalid run claim")
	}
	runs, _ := runMaps(s, b.Replication)
	terms := runTerms(s, b.Replication)
	run, ok := runs[b.RunID]
	if !ok {
		return false, errcode.E(errcode.NOT_FOUND, "run not found")
	}
	if terminalRunStatus(run.Status) || run.LeaseUntilUnix > b.UpdatedUnix || (!b.Replication && b.LeaderTerm <= terms[b.RunID]) || (b.Replication && b.LeaderTerm < terms[b.RunID]) {
		return false, nil
	}
	run.LeaseUntilUnix = b.LeaseUntilUnix
	runs[b.RunID] = run
	terms[b.RunID] = b.LeaderTerm
	if !b.Replication {
		s.syncBackupFire(b.RunID, b.LeaderTerm, b.UpdatedUnix, b.LeaseUntilUnix, "CLAIMED")
	}
	return true, nil
}

func taskMapKey(task ClusterBackupTask) string { return task.RunID + ":" + task.TaskID }

func (s *State) UpdateTask(b UpdateTaskBody) error { return s.applyUpdateTask(b) }

func (s *State) RetryFailedTasks(b RetryFailedTasksBody) error { return s.applyRetryFailedTasks(b) }

func (s *State) applyRetryFailedTasks(b RetryFailedTasksBody) error {
	s.ensure()
	if err := requireOperationID(b.OperationID); err != nil {
		return err
	}
	if b.LeaderTerm == 0 || !validMetadataString(b.RunID, maxMetadataIDLen) {
		return errcode.E(errcode.INVALID, "run id and leader term required")
	}
	if b.LeaseUntilUnix != 0 && (b.UpdatedUnix <= 0 || b.LeaseUntilUnix <= b.UpdatedUnix || b.LeaseUntilUnix > b.UpdatedUnix+int64((24*time.Hour)/time.Second)) {
		return errcode.E(errcode.INVALID, "invalid retry lease")
	}
	runs, tasks := runMaps(s, b.Replication)
	terms := runTerms(s, b.Replication)
	if _, ok := runs[b.RunID]; !ok {
		return errcode.E(errcode.NOT_FOUND, "run not found")
	}
	if term := terms[b.RunID]; term > b.LeaderTerm {
		return errcode.E(errcode.CONFLICT, "stale leader term")
	}
	retried := false
	for key, task := range tasks {
		if task.RunID != b.RunID || !retryableTaskStatus(task.Status) || (b.Replication && (task.SnapshotID == "" || task.SHA256 == "")) {
			continue
		}
		retried = true
		task.Status = "PENDING"
		if !b.Replication {
			task.SnapshotID = ""
			task.SHA256 = ""
		}
		task.Bytes = 0
		task.ErrorCode = ""
		task.ErrorSummary = ""
		task.LeaderTerm = b.LeaderTerm
		task.UpdatedUnix = b.UpdatedUnix
		tasks[key] = task
	}
	if retried {
		run := runs[b.RunID]
		run.Status = "RUNNING"
		run.Success = 0
		run.Failed = 0
		run.Unavailable = 0
		run.Timeout = 0
		run.FinishedUnix = 0
		if b.LeaseUntilUnix != 0 {
			run.LeaseUntilUnix = b.LeaseUntilUnix
		}
		runs[b.RunID] = run
		if !b.Replication {
			s.syncBackupFire(b.RunID, b.LeaderTerm, b.UpdatedUnix, run.LeaseUntilUnix, "CLAIMED")
		}
	}
	if b.LeaderTerm > terms[b.RunID] {
		terms[b.RunID] = b.LeaderTerm
	}
	return nil
}

func (s *State) applyUpdateTask(b UpdateTaskBody) error {
	s.ensure()
	if err := requireOperationID(b.OperationID); err != nil {
		return err
	}
	if b.LeaderTerm == 0 {
		return errcode.E(errcode.INVALID, "run id, task id, status, and leader term required")
	}
	if err := validateTaskMetadata(b.Task); err != nil {
		return err
	}
	if b.Task.Bytes < 0 {
		return errcode.E(errcode.INVALID, "negative task bytes")
	}
	runs, tasks := runMaps(s, b.Replication)
	terms := runTerms(s, b.Replication)
	if term := terms[b.Task.RunID]; term > b.LeaderTerm {
		return errcode.E(errcode.CONFLICT, "stale leader term")
	}
	if _, ok := runs[b.Task.RunID]; !ok {
		return errcode.E(errcode.NOT_FOUND, "run not found")
	}
	key := taskMapKey(b.Task)
	if current, ok := tasks[key]; ok {
		if b.LeaderTerm < current.LeaderTerm {
			return errcode.E(errcode.CONFLICT, "stale leader term")
		}
		if terminalTaskStatus(current.Status) {
			return nil
		}
	}
	b.Task.LeaderTerm = b.LeaderTerm
	tasks[key] = b.Task
	if b.LeaderTerm > terms[b.Task.RunID] {
		terms[b.Task.RunID] = b.LeaderTerm
	}
	aggregateTerminalRun(runs, tasks, b.Task.RunID, b.Replication)
	if !b.Replication {
		if run := runs[b.Task.RunID]; terminalRunStatus(run.Status) {
			s.syncBackupFire(run.RunID, b.LeaderTerm, 0, 0, run.Status)
		}
	}
	return nil
}

func (s *State) syncBackupFire(runID string, leaderTerm uint64, claimedUnix, leaseUntilUnix int64, status string) {
	for key, fire := range s.BackupFireLedger {
		if fire.RunID != runID {
			continue
		}
		if leaderTerm > fire.LeaderTerm {
			fire.LeaderTerm = leaderTerm
		}
		if claimedUnix != 0 {
			fire.ClaimedUnix = claimedUnix
		}
		fire.LeaseUntilUnix = leaseUntilUnix
		fire.Status = status
		s.BackupFireLedger[key] = fire
	}
}

func retryableTaskStatus(status string) bool {
	switch status {
	case "FAILED", "TIMEOUT", "UNAVAILABLE", "CONFIG_MISSING", "RETENTION_FAILED", "SKIPPED":
		return true
	default:
		return false
	}
}

func aggregateTerminalRun(runs map[string]ClusterBackupRun, tasks map[string]ClusterBackupTask, runID string, replication bool) {
	run, ok := runs[runID]
	if !ok || terminalRunStatus(run.Status) {
		return
	}
	if replication {
		aggregateReplicationTerminalRun(runs, tasks, runID)
		return
	}
	if len(run.TargetNodeIDs) == 0 {
		return
	}

	targets := make(map[string]struct{}, len(run.TargetNodeIDs))
	for _, nodeID := range run.TargetNodeIDs {
		targets[nodeID] = struct{}{}
	}
	byNode := make(map[string]ClusterBackupTask, len(targets))
	for _, task := range tasks {
		if task.RunID != runID {
			continue
		}
		if _, wanted := targets[task.NodeID]; !wanted {
			continue
		}
		current, exists := byNode[task.NodeID]
		if !exists || task.UpdatedUnix >= current.UpdatedUnix {
			byNode[task.NodeID] = task
		}
	}
	if len(byNode) != len(targets) {
		return
	}

	var success, failed, unavailable, timeout int
	var finishedUnix int64
	for nodeID := range targets {
		task := byNode[nodeID]
		if !terminalTaskStatus(task.Status) {
			return
		}
		switch task.Status {
		case "SUCCESS", "SUCCEEDED":
			success++
		case "UNAVAILABLE":
			unavailable++
		case "TIMEOUT":
			timeout++
		default:
			failed++
		}
		if task.UpdatedUnix > finishedUnix {
			finishedUnix = task.UpdatedUnix
		}
	}

	switch {
	case success == len(targets):
		run.Status = "SUCCEEDED"
	case success > 0:
		run.Status = "PARTIAL"
	default:
		run.Status = "FAILED"
	}
	run.Success = success
	run.Failed = failed
	run.Unavailable = unavailable
	run.Timeout = timeout
	run.FinishedUnix = finishedUnix
	runs[runID] = run
}

// Replication routes are identified by task ID, not target node. Several
// sources may intentionally replicate to one target, so target de-duplication
// would leave the run permanently non-terminal.
func aggregateReplicationTerminalRun(runs map[string]ClusterBackupRun, tasks map[string]ClusterBackupTask, runID string) {
	run := runs[runID]
	routeTasks := make([]ClusterBackupTask, 0)
	for _, task := range tasks {
		if task.RunID == runID {
			routeTasks = append(routeTasks, task)
		}
	}
	if len(routeTasks) == 0 {
		return
	}
	var success, failed, unavailable, timeout int
	var finished int64
	for _, task := range routeTasks {
		if !terminalTaskStatus(task.Status) {
			return
		}
		switch task.Status {
		case "SUCCESS", "SUCCEEDED":
			success++
		case "UNAVAILABLE":
			unavailable++
		case "TIMEOUT":
			timeout++
		default:
			failed++
		}
		if task.UpdatedUnix > finished {
			finished = task.UpdatedUnix
		}
	}
	if success == len(routeTasks) {
		run.Status = "SUCCEEDED"
	} else if success > 0 {
		run.Status = "PARTIAL"
	} else {
		run.Status = "FAILED"
	}
	run.Success, run.Failed, run.Unavailable, run.Timeout, run.FinishedUnix = success, failed, unavailable, timeout, finished
	runs[runID] = run
}

func (s *State) FinishRun(b FinishRunBody) error { return s.applyFinishRun(b) }

func (s *State) applyFinishRun(b FinishRunBody) error {
	s.ensure()
	if err := requireOperationID(b.OperationID); err != nil {
		return err
	}
	if b.LeaderTerm == 0 || !validMetadataString(b.RunID, maxMetadataIDLen) || !validRunStatus(b.Status) {
		return errcode.E(errcode.INVALID, "run id, status, and leader term required")
	}
	if b.Success < 0 || b.Failed < 0 || b.Unavailable < 0 || b.Timeout < 0 {
		return errcode.E(errcode.INVALID, "negative run counters")
	}
	runs, tasks := runMaps(s, b.Replication)
	terms := runTerms(s, b.Replication)
	run, ok := runs[b.RunID]
	if !ok {
		return errcode.E(errcode.NOT_FOUND, "run not found")
	}
	if term := terms[b.RunID]; term > b.LeaderTerm {
		return errcode.E(errcode.CONFLICT, "stale leader term")
	}
	for _, task := range tasks {
		if task.RunID == b.RunID && task.LeaderTerm > b.LeaderTerm {
			return errcode.E(errcode.CONFLICT, "stale leader term")
		}
	}
	if terminalRunStatus(run.Status) {
		return nil
	}
	run.Status, run.Success, run.Failed, run.Unavailable, run.Timeout, run.FinishedUnix = b.Status, b.Success, b.Failed, b.Unavailable, b.Timeout, b.FinishedUnix
	runs[b.RunID] = run
	if !b.Replication && terminalRunStatus(run.Status) {
		s.syncBackupFire(run.RunID, b.LeaderTerm, 0, 0, run.Status)
	}
	if b.LeaderTerm > terms[b.RunID] {
		terms[b.RunID] = b.LeaderTerm
	}
	return nil
}

func (s *State) PruneRunMetadata(beforeUnix int64) {
	s.ensure()
	prune := func(runs map[string]ClusterBackupRun, tasks map[string]ClusterBackupTask, terms map[string]uint64) {
		for id, run := range runs {
			if terminalRunStatus(run.Status) && run.FinishedUnix < beforeUnix {
				delete(runs, id)
				delete(terms, id)
				for key, task := range tasks {
					if task.RunID == id {
						delete(tasks, key)
					}
				}
			}
		}
	}
	prune(s.BackupRuns, s.BackupTasks, s.BackupRunTerms)
	prune(s.ReplicationRuns, s.ReplicationTasks, s.ReplicationRunTerms)
	for key, fire := range s.BackupFireLedger {
		if (fire.LeaseUntilUnix > 0 && fire.LeaseUntilUnix <= beforeUnix) || (terminalRunStatus(fire.Status) && fire.ScheduledUnix < beforeUnix) {
			delete(s.BackupFireLedger, key)
		}
	}
}

func (s *State) applyPruneRunMetadata(b PruneRunMetadataBody) error {
	if err := requireOperationID(b.OperationID); err != nil {
		return err
	}
	if b.BeforeUnix <= 0 {
		return errcode.E(errcode.INVALID, "before unix required")
	}
	s.PruneRunMetadata(b.BeforeUnix)
	return nil
}

func (s *State) applyReplicationPolicyPut(b ReplicationPolicyPutBody) error {
	if err := requireOperationID(b.OperationID); err != nil {
		return err
	}
	name := strings.TrimSpace(b.Name)
	if b.PolicyID == "" || name == "" {
		return errcode.E(errcode.INVALID, "policy id and name required")
	}
	for id, policy := range s.ReplicationPolicies {
		if id != b.PolicyID && policy.Name == name {
			return errcode.E(errcode.CONFLICT, "replication policy name already exists")
		}
	}
	if err := validateTargetIDs(b.SourceSelector, b.SourceIDs); err != nil {
		return errcode.E(errcode.INVALID, "source ids")
	}
	if err := s.validateExplicitTargets(b.SourceSelector, b.SourceIDs); err != nil {
		return errcode.E(errcode.INVALID, "source node not admitted")
	}
	if err := s.validateAgentGroups(b.SourceSelector, b.SourceIDs); err != nil {
		return errcode.E(errcode.INVALID, "source agent group")
	}
	if b.ReplicaFactor <= 0 {
		return errcode.E(errcode.INVALID, "replica factor")
	}
	if b.RetentionKeepLast < 0 || b.RetentionKeepDays < 0 || b.RetentionMaxBytes < 0 || b.MaxConcurrency < 0 || b.BandwidthLimit < 0 {
		return errcode.E(errcode.INVALID, "invalid retention or limits")
	}
	if !validReplicationTrigger(b.Trigger) {
		return errcode.E(errcode.INVALID, "trigger")
	}
	if b.Trigger == "SCHEDULE" {
		if strings.TrimSpace(b.ScheduleCron) == "" {
			return errcode.E(errcode.INVALID, "schedule cron required")
		}
		if err := validatePolicySchedule(b.ScheduleCron, b.Timezone); err != nil {
			return err
		}
	} else if b.ScheduleCron != "" || b.Timezone != "" {
		if err := validatePolicySchedule(b.ScheduleCron, b.Timezone); err != nil {
			return err
		}
	}
	if b.Trigger == "AFTER_PRIMARY_BACKUP" {
		if err := s.validatePrimaryPolicies(b.PrimaryPolicyIDs); err != nil {
			return err
		}
	}
	seenSources := map[string]struct{}{}
	for _, route := range b.Routes {
		source := strings.TrimSpace(route.SourceNodeID)
		if source == "" || source != route.SourceNodeID {
			return errcode.E(errcode.INVALID, "route source")
		}
		if _, ok := seenSources[source]; ok {
			return errcode.E(errcode.INVALID, "duplicate route source")
		}
		if !s.isAdmittedMember(source) {
			return errcode.E(errcode.INVALID, "route source not admitted")
		}
		seenSources[source] = struct{}{}
		seenTargets := map[string]struct{}{}
		for _, rawTarget := range route.TargetNodeIDs {
			target := strings.TrimSpace(rawTarget)
			if target == "" || target != rawTarget {
				return errcode.E(errcode.INVALID, "route target")
			}
			if target == source {
				return errcode.E(errcode.INVALID, "self replication")
			}
			if _, ok := seenTargets[target]; ok {
				return errcode.E(errcode.INVALID, "duplicate target")
			}
			if !s.isAdmittedMember(target) {
				return errcode.E(errcode.INVALID, "route target not admitted")
			}
			seenTargets[target] = struct{}{}
		}
	}
	cur, exists := s.ReplicationPolicies[b.PolicyID]
	if b.ExpectedRevision >= 0 {
		if !exists {
			return errcode.E(errcode.CONFLICT, "policy not found for expected revision")
		}
		if cur.Revision != b.ExpectedRevision {
			return errcode.E(errcode.CONFLICT, "revision mismatch")
		}
	}
	if len(b.Routes) > 0 {
		availableTargets := s.admittedMemberCount() - 1
		if availableTargets < 0 || b.ReplicaFactor > availableTargets {
			return errcode.E(errcode.INVALID, "replica factor exceeds candidate count")
		}
		for _, route := range b.Routes {
			if len(route.TargetNodeIDs) != b.ReplicaFactor {
				return errcode.E(errcode.INVALID, "route target count does not match replica factor")
			}
		}
	}
	cur.PolicyID, cur.Name, cur.Enabled = b.PolicyID, name, b.Enabled
	cur.SourceSelector, cur.SourceIDs, cur.ReplicaFactor = b.SourceSelector, append([]string(nil), b.SourceIDs...), b.ReplicaFactor
	cur.Routes = append([]ReplicationRoute(nil), b.Routes...)
	for i := range cur.Routes {
		cur.Routes[i].TargetNodeIDs = append([]string(nil), cur.Routes[i].TargetNodeIDs...)
	}
	cur.Trigger, cur.PrimaryPolicyIDs = b.Trigger, append([]string(nil), b.PrimaryPolicyIDs...)
	cur.ScheduleCron, cur.Timezone = b.ScheduleCron, b.Timezone
	cur.RetentionKeepLast, cur.RetentionKeepDays, cur.RetentionMaxBytes = b.RetentionKeepLast, b.RetentionKeepDays, b.RetentionMaxBytes
	cur.MaxConcurrency, cur.VerifyAfterCopy, cur.BandwidthLimit = b.MaxConcurrency, b.VerifyAfterCopy, b.BandwidthLimit
	cur.TopologyConstraints = mapsClone(b.TopologyConstraints)
	cur.Revision++
	s.ReplicationPolicies[b.PolicyID] = cur
	return nil
}

func validReplicationTrigger(trigger string) bool {
	switch trigger {
	case "AFTER_PRIMARY_BACKUP", "SCHEDULE", "MANUAL":
		return true
	default:
		return false
	}
}

func (s *State) validatePrimaryPolicies(ids []string) error {
	if len(ids) == 0 {
		return errcode.E(errcode.INVALID, "primary policy ids required")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || trimmed != id {
			return errcode.E(errcode.INVALID, "primary policy id")
		}
		if _, ok := seen[trimmed]; ok {
			return errcode.E(errcode.INVALID, "duplicate primary policy")
		}
		if _, ok := s.BackupPolicies[trimmed]; !ok {
			return errcode.E(errcode.INVALID, "primary backup policy not found")
		}
		seen[trimmed] = struct{}{}
	}
	return nil
}

func (s *State) isAdmittedMember(nodeID string) bool {
	m, ok := s.Members[nodeID]
	return ok && m.Status == MemberAdmitted
}

func (s *State) computeCandidateCount(selector string, selectorIDs []string) int {
	switch selector {
	case "ALL_ADMITTED":
		count := 0
		for _, member := range s.Members {
			if member.Status == MemberAdmitted {
				count++
			}
		}
		return count
	case "EXPLICIT_NODES":
		return len(selectorIDs)
	case "AGENT_GROUP":
		allowed := map[string]struct{}{}
		for _, groupID := range selectorIDs {
			group, ok := s.AgentGroups[groupID]
			if !ok {
				continue
			}
			for _, nodeID := range group.MemberIDs {
				if s.isAdmittedMember(nodeID) {
					allowed[nodeID] = struct{}{}
				}
			}
		}
		return len(allowed)
	default:
		return 0
	}
}

func (s *State) admittedMemberCount() int {
	count := 0
	for _, member := range s.Members {
		if member.Status == MemberAdmitted {
			count++
		}
	}
	return count
}

func mapsClone(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *State) applyReplicationPolicyDelete(b ReplicationPolicyDeleteBody) error {
	if err := requireOperationID(b.OperationID); err != nil {
		return err
	}
	if _, ok := s.ReplicationPolicies[b.PolicyID]; !ok {
		return errcode.E(errcode.NOT_FOUND, "replication policy not found")
	}
	delete(s.ReplicationPolicies, b.PolicyID)
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
	"replication.read", "replication.manage",
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
