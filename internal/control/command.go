package control

import "encoding/json"

const (
	CmdBootstrap                  = "bootstrap"
	CmdUserPut                    = "user_put"
	CmdUserDisable                = "user_disable"
	CmdUserPasswordSet            = "user_password_set"
	CmdLoginOK                    = "login_ok"
	CmdLoginFail                  = "login_fail"
	CmdSessionPut                 = "session_put"
	CmdSessionDel                 = "session_del"
	CmdTokenPut                   = "token_put"
	CmdTokenRevoke                = "token_revoke"
	CmdRolePut                    = "role_put"
	CmdRoleDelete                 = "role_delete"
	CmdBindPut                    = "bind_put"
	CmdBindDelete                 = "bind_delete"
	CmdJoinTokenPut               = "join_token_put"
	CmdJoinTokenConsume           = "join_token_consume"
	CmdJoinTokenRevoke            = "join_token_revoke"
	CmdMemberPut                  = "member_put"
	CmdMemberRemove               = "member_remove"
	CmdCRLAdd                     = "crl_add"
	CmdGroupPut                   = "group_put"
	CmdGroupDelete                = "group_delete"
	CmdGroupMemberAdd             = "group_member_add"
	CmdGroupMemberRemove          = "group_member_remove"
	CmdAlertChannelPut            = "alert_channel_put"
	CmdAlertChannelDelete         = "alert_channel_delete"
	CmdAlertPolicyPut             = "alert_policy_put"
	CmdBackupPolicyPut            = "backup_policy_put"
	CmdBackupPolicyDelete         = "backup_policy_delete"
	CmdReplicationPolicyPut       = "replication_policy_put"
	CmdReplicationPolicyDelete    = "replication_policy_delete"
	CmdReplicationDeleteIntentPut = "replication_delete_intent_put"
	CmdBackupFireClaim            = "backup_fire_claim"
	CmdBackupScheduledRunClaim    = "backup_scheduled_run_claim"
	CmdBackupRunCreate            = "backup_run_create"
	CmdBackupRunClaim             = "backup_run_claim"
	CmdBackupTaskUpdate           = "backup_task_update"
	CmdBackupRetryFailedTasks     = "backup_retry_failed_tasks"
	CmdBackupRunFinish            = "backup_run_finish"
	CmdRunMetadataPrune           = "run_metadata_prune"
)

// Command is a Raft log payload: type + JSON body.
type Command struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

type BootstrapBody struct {
	ClusterID    string `json:"cluster_id"`
	AdminUser    string `json:"admin_user"`
	PasswordHash string `json:"password_hash"`
	AdminUserID  string `json:"admin_user_id"`
	NowUnix      int64  `json:"now_unix"`
}

type UserPutBody struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
}

type UserDisableBody struct {
	UserID string `json:"user_id"`
}

type UserPasswordSetBody struct {
	UserID       string `json:"user_id"`
	PasswordHash string `json:"password_hash"`
}

type LoginOKBody struct {
	Username string `json:"username"`
}

type LoginFailBody struct {
	Username string `json:"username"`
}

type SessionPutBody struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	CSRF        string `json:"csrf"`
	ExpiresUnix int64  `json:"expires_unix"`
}

type SessionDelBody struct {
	ID string `json:"id"`
}

type TokenPutBody struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	Name        string `json:"name"`
	Hash        string `json:"hash"`
	ExpiresUnix int64  `json:"expires_unix"`
}

type TokenRevokeBody struct {
	ID string `json:"id"`
}

type RolePutBody struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Perms        []string `json:"perms"`
	ExistingOnly bool     `json:"existing_only,omitempty"`
}

type RoleDeleteBody struct {
	ID string `json:"id"`
}

type BindPutBody struct {
	UserID  string    `json:"user_id"`
	RoleID  string    `json:"role_id"`
	ScopeID string    `json:"scope_id"`
	Scope   ScopeType `json:"scope"`
}

type BindDeleteBody struct {
	UserID  string    `json:"user_id"`
	RoleID  string    `json:"role_id"`
	ScopeID string    `json:"scope_id"`
	Scope   ScopeType `json:"scope"`
}

type JoinTokenPutBody struct {
	ID         string `json:"id"`
	Hash       string `json:"hash"`
	TTLSeconds int64  `json:"ttl_seconds"`
	Remaining  int    `json:"remaining"`
}

type JoinTokenConsumeBody struct {
	Plain string `json:"plain"`
}

type JoinTokenRevokeBody struct {
	ID string `json:"id"`
}

type MemberPutBody struct {
	NodeID     string       `json:"node_id"`
	RaftAddr   string       `json:"raft_addr"`
	CertSerial string       `json:"cert_serial"`
	Status     MemberStatus `json:"status"`
}

type MemberRemoveBody struct {
	NodeID string `json:"node_id"`
}

type CRLAddBody struct {
	Serial string `json:"serial"`
}

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

type AlertChannel struct {
	ChannelID   string `json:"channel_id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	ConfigJSON  string `json:"config_json,omitempty"`
	CreatedUnix int64  `json:"created_unix"`
	UpdatedUnix int64  `json:"updated_unix"`
}

type AlertPolicy struct {
	DedupWindowSec      int64 `json:"dedup_window_sec"`
	NotifyOnResolve     bool  `json:"notify_on_resolve"`
	CPUHighPercent      int   `json:"cpu_high_percent"`
	MemoryHighPercent   int   `json:"memory_high_percent"`
	DiskHighPercent     int   `json:"disk_high_percent"`
	HighConsecutiveMins int   `json:"high_consecutive_mins"`
	SuspectTooLongSec   int64 `json:"suspect_too_long_sec"`
}

func DefaultAlertPolicy() AlertPolicy {
	return AlertPolicy{
		DedupWindowSec:      600,
		NotifyOnResolve:     true,
		CPUHighPercent:      90,
		MemoryHighPercent:   90,
		DiskHighPercent:     90,
		HighConsecutiveMins: 2,
		SuspectTooLongSec:   120,
	}
}

type AlertChannelPutBody struct {
	ChannelID, Type, Name, ConfigJSON string
	Enabled                           bool
	NowUnix                           int64
}

type AlertChannelDeleteBody struct{ ChannelID string }

type AlertPolicyPutBody struct {
	DedupWindowSec                                     int64
	NotifyOnResolve                                    bool
	CPUHighPercent, MemoryHighPercent, DiskHighPercent int
	HighConsecutiveMins                                int
	SuspectTooLongSec                                  int64
}

type BackupPolicyPutBody struct {
	OperationID              string `json:"operation_id,omitempty"`
	PolicyID, Name           string
	Enabled                  bool
	ScheduleCron, Timezone   string
	TargetSelector           string
	TargetIDs                []string
	Sink, DestinationProfile string
	RetentionKeepLast        int
	RetentionKeepDays        int
	RetentionMaxBytes        int64
	TimeoutSeconds           int
	MaxConcurrency           int
	UnavailablePolicy        string
}

type BackupPolicyDeleteBody struct {
	OperationID string `json:"operation_id,omitempty"`
	PolicyID    string `json:"policy_id"`
}

type ReplicationRoute struct {
	SourceNodeID  string   `json:"source_node_id"`
	TargetNodeIDs []string `json:"target_node_ids"`
}

type ReplicationPolicyPutBody struct {
	OperationID            string `json:"operation_id,omitempty"`
	PolicyID, Name         string
	Enabled                bool
	SourceSelector         string
	SourceIDs              []string
	ReplicaFactor          int
	Routes                 []ReplicationRoute
	Trigger                string
	PrimaryPolicyIDs       []string
	ScheduleCron, Timezone string
	RetentionKeepLast      int
	RetentionKeepDays      int
	RetentionMaxBytes      int64
	MaxConcurrency         int
	VerifyAfterCopy        bool
	BandwidthLimit         int64
	TopologyConstraints    map[string]string
	ExpectedRevision       int64 `json:"expected_revision,omitempty"`
}

type ReplicationPolicyDeleteBody struct {
	OperationID string `json:"operation_id,omitempty"`
	PolicyID    string `json:"policy_id"`
}

// ReplicationDeleteIntent is metadata-only authorization for an exact peer retention delete.
type ReplicationDeleteIntent struct {
	IntentID       string `json:"intent_id"`
	PolicyID       string `json:"policy_id"`
	PolicyRevision int64  `json:"policy_revision"`
	SourceNodeID   string `json:"source_node_id"`
	TargetNodeID   string `json:"target_node_id"`
	SnapshotID     string `json:"snapshot_id"`
	LeaderTerm     uint64 `json:"leader_term"`
	ExpiresUnix    int64  `json:"expires_unix"`
	Status         string `json:"status"`
}

type ReplicationDeleteIntentPutBody struct {
	OperationID string                  `json:"operation_id"`
	Intent      ReplicationDeleteIntent `json:"intent"`
}

// FireClaimBody records an idempotent scheduled-fire claim. LeaseUntilUnix is
// optional; zero uses the control plane's short default lease unless Durable
// is set, in which case the key is kept with a zero lease and is not taken over.
type FireClaimBody struct {
	OperationID    string `json:"operation_id"`
	FireKey        string `json:"fire_key"`
	PolicyID       string `json:"policy_id"`
	ScheduledUnix  int64  `json:"scheduled_unix"`
	LeaseUntilUnix int64  `json:"lease_until_unix"`
	LeaderTerm     uint64 `json:"leader_term"`
	Status         string `json:"status,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	Durable        bool   `json:"durable,omitempty"`
}

// ScheduledRunClaimBody atomically records a scheduled fire and its frozen
// run metadata in one Raft FSM mutation.
type ScheduledRunClaimBody struct {
	Fire FireClaimBody    `json:"fire"`
	Run  ClusterBackupRun `json:"run"`
}

type CreateRunBody struct {
	OperationID string              `json:"operation_id"`
	Run         ClusterBackupRun    `json:"run"`
	Tasks       []ClusterBackupTask `json:"tasks,omitempty"`
	LeaderTerm  uint64              `json:"leader_term"`
	Replication bool                `json:"replication"`
}

type RunClaimBody struct {
	OperationID    string `json:"operation_id"`
	RunID          string `json:"run_id"`
	LeaderTerm     uint64 `json:"leader_term"`
	UpdatedUnix    int64  `json:"updated_unix"`
	LeaseUntilUnix int64  `json:"lease_until_unix"`
	Replication    bool   `json:"replication"`
}

type UpdateTaskBody struct {
	OperationID string            `json:"operation_id"`
	Task        ClusterBackupTask `json:"task"`
	LeaderTerm  uint64            `json:"leader_term"`
	Replication bool              `json:"replication"`
}

type RetryFailedTasksBody struct {
	OperationID    string `json:"operation_id"`
	RunID          string `json:"run_id"`
	LeaderTerm     uint64 `json:"leader_term"`
	UpdatedUnix    int64  `json:"updated_unix"`
	LeaseUntilUnix int64  `json:"lease_until_unix,omitempty"`
	Replication    bool   `json:"replication"`
}

type FinishRunBody struct {
	OperationID  string `json:"operation_id"`
	RunID        string `json:"run_id"`
	Status       string `json:"status"`
	Success      int    `json:"success"`
	Failed       int    `json:"failed"`
	Unavailable  int    `json:"unavailable"`
	Timeout      int    `json:"timeout"`
	FinishedUnix int64  `json:"finished_unix"`
	LeaderTerm   uint64 `json:"leader_term"`
	Replication  bool   `json:"replication"`
}

type PruneRunMetadataBody struct {
	OperationID string `json:"operation_id"`
	BeforeUnix  int64  `json:"before_unix"`
}

// EncodeCommand marshals body as the Command payload.
func EncodeCommand(typ string, body any) (Command, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Command{}, err
	}
	return Command{Type: typ, Body: raw}, nil
}
