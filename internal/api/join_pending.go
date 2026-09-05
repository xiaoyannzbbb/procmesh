package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

const pendingJoinFile = "join.pending.json"

type pendingJoin struct {
	OperationID string `json:"operation_id"`
	NodeID      string `json:"node_id"`
	SeedServer  string `json:"seed_server"`
	TokenHash   string `json:"token_sha256"`
	CSRPEM      []byte `json:"csr_pem"`
	KeyPEM      []byte `json:"key_pem"`
}

func loadOrCreatePendingJoin(dir, nodeID, seedServer, token, operationID string) (pendingJoin, error) {
	path := filepath.Join(dir, pendingJoinFile)
	normalizedSeed := seedBaseURL(seedServer)
	tokenHash := pendingJoinTokenHash(token)
	raw, err := os.ReadFile(path)
	if err == nil {
		var pending pendingJoin
		if jsonErr := json.Unmarshal(raw, &pending); jsonErr != nil {
			return pendingJoin{}, errcode.Wrap(errcode.INVALID, "parse pending join", jsonErr)
		}
		if pending.OperationID == "" || pending.NodeID == "" || len(pending.CSRPEM) == 0 || len(pending.KeyPEM) == 0 {
			return pendingJoin{}, errcode.E(errcode.INVALID, "pending join is incomplete")
		}
		if pending.NodeID != nodeID || pending.SeedServer != normalizedSeed || pending.TokenHash != tokenHash {
			return pendingJoin{}, errcode.E(errcode.CONFLICT, "different join already pending")
		}
		return pending, nil
	}
	if !os.IsNotExist(err) {
		return pendingJoin{}, fmt.Errorf("read pending join: %w", err)
	}

	csrPEM, keyPEM, err := control.NewCSR("join", nodeID)
	if err != nil {
		return pendingJoin{}, err
	}
	pending := pendingJoin{
		OperationID: operationID,
		NodeID:      nodeID,
		SeedServer:  normalizedSeed,
		TokenHash:   tokenHash,
		CSRPEM:      csrPEM,
		KeyPEM:      keyPEM,
	}
	doc, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return pendingJoin{}, fmt.Errorf("marshal pending join: %w", err)
	}
	if err := writeAtomicPrivateFile(path, append(doc, '\n')); err != nil {
		return pendingJoin{}, err
	}
	return pending, nil
}

func pendingJoinTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func joinErrorRetryable(err error) bool {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return true
	}
	info, ok := procMeshErrorInfo(connectErr)
	if !ok {
		// Without an authoritative service error, the client cannot know whether
		// join_prepare committed before the request failed or was canceled.
		return true
	}
	switch errcode.Code(info.GetCode()) {
	case errcode.INVALID, errcode.INVALID_CREDENTIALS, errcode.ACCOUNT_LOCKED,
		errcode.DENIED, errcode.DUPLICATE_NODE_ID, errcode.INCOMPATIBLE_VERSION,
		errcode.CONFLICT, errcode.NOT_FOUND:
		return false
	default:
		return true
	}
}

func writeAtomicPrivateFile(path string, data []byte) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir pending join: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".join.pending-*")
	if err != nil {
		return fmt.Errorf("create pending join: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod pending join: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write pending join: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync pending join: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close pending join: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("commit pending join: %w", err)
	}
	return syncDirectory(dir)
}

func removePendingJoin(dir string) error {
	path := filepath.Join(dir, pendingJoinFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove pending join: %w", err)
	}
	return syncDirectory(dir)
}

func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}
