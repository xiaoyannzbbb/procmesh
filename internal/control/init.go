package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

const (
	metaFileName   = "cluster.json"
	secretFileName = "secret"
	adminFileName  = "admin.bootstrap"
	defaultAdmin   = "admin"
	adminPasswordN = 20
	secretBytes    = 32
	metaFileMode   = 0o640
	secretFileMode = 0o600
	adminFileMode  = 0o600
)

// Meta is persisted as cluster.json under the cluster data directory.
type Meta struct {
	ClusterID     string   `json:"cluster_id"`
	NodeID        string   `json:"node_id"`
	ControlMember bool     `json:"control_member"`
	CreatedAt     string   `json:"created_at"`
	GossipSeeds   []string `json:"gossip_seeds,omitempty"`
}

// InitResult is returned once with the plaintext admin password.
type InitResult struct {
	ClusterID     string
	NodeID        string
	AdminUser     string
	AdminPassword string
}

type adminBootstrap struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

// Init creates CA/agent PEMs, cluster secret, admin bootstrap, and cluster.json.
// Does not start Raft; sets ControlMember=true in meta only.
func Init(dir, nodeID, adminUser string, now time.Time) (InitResult, error) {
	if AlreadyInited(dir) {
		return InitResult{}, errcode.E(errcode.CONFLICT, "cluster already initialized")
	}
	if adminUser == "" {
		adminUser = defaultAdmin
	}

	clusterID, err := newUUID()
	if err != nil {
		return InitResult{}, err
	}

	bundle, err := NewBundle(clusterID, nodeID, now)
	if err != nil {
		return InitResult{}, err
	}
	if err := WriteBundle(dir, bundle); err != nil {
		return InitResult{}, err
	}

	var secretRaw [secretBytes]byte
	if _, err := rand.Read(secretRaw[:]); err != nil {
		return InitResult{}, fmt.Errorf("generate secret: %w", err)
	}
	secretHex := hex.EncodeToString(secretRaw[:])
	if err := writeFile(filepath.Join(dir, secretFileName), []byte(secretHex+"\n"), secretFileMode); err != nil {
		return InitResult{}, err
	}

	password, err := RandomPassword(adminPasswordN)
	if err != nil {
		return InitResult{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return InitResult{}, err
	}
	adminDoc, err := json.Marshal(adminBootstrap{
		Username:     adminUser,
		PasswordHash: hash,
	})
	if err != nil {
		return InitResult{}, fmt.Errorf("marshal admin bootstrap: %w", err)
	}
	if err := writeFile(filepath.Join(dir, adminFileName), append(adminDoc, '\n'), adminFileMode); err != nil {
		return InitResult{}, err
	}

	meta := Meta{
		ClusterID:     clusterID,
		NodeID:        nodeID,
		ControlMember: true,
		CreatedAt:     now.UTC().Format(time.RFC3339),
	}
	metaDoc, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return InitResult{}, fmt.Errorf("marshal cluster meta: %w", err)
	}
	if err := writeFile(filepath.Join(dir, metaFileName), append(metaDoc, '\n'), metaFileMode); err != nil {
		return InitResult{}, err
	}

	return InitResult{
		ClusterID:     clusterID,
		NodeID:        nodeID,
		AdminUser:     adminUser,
		AdminPassword: password,
	}, nil
}

// LoadAdminBootstrap reads dir/admin.bootstrap. Missing file returns errcode.NOT_FOUND.
func LoadAdminBootstrap(dir string) (username, passwordHash string, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, adminFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", errcode.E(errcode.NOT_FOUND, "admin bootstrap not found")
		}
		return "", "", fmt.Errorf("read admin bootstrap: %w", err)
	}
	var doc adminBootstrap
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", "", fmt.Errorf("parse admin bootstrap: %w", err)
	}
	return doc.Username, doc.PasswordHash, nil
}

// LoadMeta reads cluster.json. Missing file returns errcode.NOT_FOUND.
func LoadMeta(dir string) (Meta, error) {
	raw, err := os.ReadFile(filepath.Join(dir, metaFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{}, errcode.E(errcode.NOT_FOUND, "cluster not initialized")
		}
		return Meta{}, fmt.Errorf("read cluster meta: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, fmt.Errorf("parse cluster meta: %w", err)
	}
	return m, nil
}

// SaveMeta rewrites cluster.json.
func SaveMeta(dir string, meta Meta) error {
	metaDoc, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cluster meta: %w", err)
	}
	return writeFile(filepath.Join(dir, metaFileName), append(metaDoc, '\n'), metaFileMode)
}

// AppendGossipSeed adds addr to cluster.json gossip_seeds if it is not already present.
func AppendGossipSeed(dir, addr string) error {
	if addr == "" {
		return nil
	}
	meta, err := LoadMeta(dir)
	if err != nil {
		return err
	}
	for _, s := range meta.GossipSeeds {
		if s == addr {
			return nil
		}
	}
	meta.GossipSeeds = append(meta.GossipSeeds, addr)
	return SaveMeta(dir, meta)
}

// AlreadyInited reports whether cluster.json exists under dir.
func AlreadyInited(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, metaFileName))
	return err == nil && !st.IsDir()
}

// RollbackInit removes artifacts created by an Init whose cluster identity has
// not been published. The cluster ID check prevents one failed request from
// deleting a different, completed initialization.
func RollbackInit(dir, clusterID string) error {
	meta, err := LoadMeta(dir)
	if err != nil {
		return err
	}
	if clusterID == "" || meta.ClusterID != clusterID {
		return errcode.E(errcode.CONFLICT, "cluster initialization changed")
	}
	files := []string{
		metaFileName,
		adminFileName,
		secretFileName,
		caCertFile,
		caKeyFile,
		agentCertFile,
		agentKeyFile,
	}
	var cleanupErr error
	for _, name := range files {
		if removeErr := os.Remove(filepath.Join(dir, name)); removeErr != nil && !os.IsNotExist(removeErr) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove %s: %w", name, removeErr))
		}
	}
	return cleanupErr
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	// WriteFile is umask-sensitive; force exact perms.
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", filepath.Base(path), err)
	}
	return nil
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
