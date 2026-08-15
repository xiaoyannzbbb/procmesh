package control

import "encoding/json"

const (
	CmdBootstrap        = "bootstrap"
	CmdUserPut          = "user_put"
	CmdUserDisable      = "user_disable"
	CmdLoginOK          = "login_ok"
	CmdLoginFail        = "login_fail"
	CmdSessionPut       = "session_put"
	CmdSessionDel       = "session_del"
	CmdTokenPut         = "token_put"
	CmdTokenRevoke      = "token_revoke"
	CmdRolePut          = "role_put"
	CmdBindPut          = "bind_put"
	CmdJoinTokenPut     = "join_token_put"
	CmdJoinTokenConsume = "join_token_consume"
	CmdJoinTokenRevoke  = "join_token_revoke"
	CmdMemberPut        = "member_put"
	CmdMemberRemove     = "member_remove"
	CmdCRLAdd           = "crl_add"
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
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Perms []string `json:"perms"`
}

type BindPutBody struct {
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

// EncodeCommand marshals body as the Command payload.
func EncodeCommand(typ string, body any) (Command, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Command{}, err
	}
	return Command{Type: typ, Body: raw}, nil
}
