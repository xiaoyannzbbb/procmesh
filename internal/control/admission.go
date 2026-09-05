package control

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

const admissionApplyTO = 5 * time.Second

// Admission issues and consumes join tokens / members via Raft.
// Sign 用的 CA 仅 leader 有。
type Admission struct {
	Node *Node
}

type JoinPrepare struct {
	OperationID string
	Token       string
	NodeID      string
	RaftAddr    string
	CSRHash     string
	CertPEM     []byte
	CertSerial  string
}

func (a *Admission) CreateToken(ttl time.Duration, uses int, now time.Time) (plain string, info TokenInfo, err error) {
	if err := a.requireNode(); err != nil {
		return "", TokenInfo{}, err
	}
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	if uses <= 0 {
		uses = DefaultTokenUses
	}
	var raw [tokenPlainN]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", TokenInfo{}, fmt.Errorf("generate token: %w", err)
	}
	plain = tokenPrefix + hex.EncodeToString(raw[:])
	id, err := newUUID()
	if err != nil {
		return "", TokenInfo{}, err
	}
	_ = now // FSM 用 log AppendedAt 加 TTL 计算过期时间
	cmd, err := EncodeCommand(CmdJoinTokenPut, JoinTokenPutBody{
		ID:         id,
		Hash:       hashToken(plain),
		TTLSeconds: int64(ttl.Seconds()),
		Remaining:  uses,
	})
	if err != nil {
		return "", TokenInfo{}, err
	}
	if err := a.Node.Apply(cmd, admissionApplyTO); err != nil {
		return "", TokenInfo{}, err
	}
	// ExpiresAt 是近似值；真实值由 FSM Apply 时的 AppendedAt 决定
	return plain, TokenInfo{
		ID:        id,
		ExpiresAt: time.Now().Add(ttl),
		Remaining: uses,
	}, nil
}

func (a *Admission) ConsumeToken(plain string, now time.Time) error {
	if err := a.requireNode(); err != nil {
		return err
	}
	_ = now // FSM 用 log AppendedAt 判过期
	cmd, err := EncodeCommand(CmdJoinTokenConsume, JoinTokenConsumeBody{Hash: hashToken(plain)})
	if err != nil {
		return err
	}
	return a.Node.Apply(cmd, admissionApplyTO)
}

func (a *Admission) RevokeToken(id string) error {
	if err := a.requireNode(); err != nil {
		return err
	}
	cmd, err := EncodeCommand(CmdJoinTokenRevoke, JoinTokenRevokeBody{ID: id})
	if err != nil {
		return err
	}
	return a.Node.Apply(cmd, admissionApplyTO)
}

func (a *Admission) PrepareJoin(in JoinPrepare) (JoinAttempt, error) {
	if err := a.requireNode(); err != nil {
		return JoinAttempt{}, err
	}
	cmd, err := EncodeCommand(CmdJoinPrepare, JoinPrepareBody{
		OperationID: in.OperationID,
		TokenHash:   hashToken(in.Token),
		NodeID:      in.NodeID,
		RaftAddr:    in.RaftAddr,
		CSRHash:     in.CSRHash,
		CertPEM:     in.CertPEM,
		CertSerial:  in.CertSerial,
	})
	if err != nil {
		return JoinAttempt{}, err
	}
	if err := a.Node.Apply(cmd, admissionApplyTO); err != nil {
		return JoinAttempt{}, err
	}
	attempt, ok := a.Node.View().JoinAttempts[in.NodeID]
	if !ok || attempt.OperationID != in.OperationID {
		return JoinAttempt{}, errcode.E(errcode.UNAVAILABLE, "join preparation not committed")
	}
	return attempt, nil
}

func (a *Admission) CompleteJoin(operationID, nodeID string) error {
	if err := a.requireNode(); err != nil {
		return err
	}
	cmd, err := EncodeCommand(CmdJoinComplete, JoinCompleteBody{OperationID: operationID, NodeID: nodeID})
	if err != nil {
		return err
	}
	return a.Node.Apply(cmd, admissionApplyTO)
}

func (a *Admission) Admit(nodeID, raftAddr, certSerial string) error {
	if err := a.requireNode(); err != nil {
		return err
	}
	cmd, err := EncodeCommand(CmdMemberPut, MemberPutBody{
		NodeID:     nodeID,
		RaftAddr:   raftAddr,
		CertSerial: certSerial,
		Status:     MemberAdmitted,
	})
	if err != nil {
		return err
	}
	return a.Node.Apply(cmd, admissionApplyTO)
}

func (a *Admission) IsRevoked(nodeID string) bool {
	if a == nil || a.Node == nil {
		return false
	}
	view := a.Node.View()
	m, ok := view.Member(nodeID)
	if !ok {
		return false
	}
	return m.Status == MemberRemoved || m.Status == MemberRevoked
}

func (a *Admission) requireNode() error {
	if a == nil || a.Node == nil {
		return errcode.E(errcode.UNAVAILABLE, "raft control not configured")
	}
	return nil
}
