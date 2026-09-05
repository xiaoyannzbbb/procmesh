package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

var caKeyInstallMu sync.Mutex

type CapabilityChallenge struct {
	OperationID   string `json:"operation_id"`
	NodeID        string `json:"node_id"`
	CertSerial    string `json:"cert_serial"`
	CAFingerprint string `json:"ca_spki_sha256"`
	Epoch         uint64 `json:"epoch"`
	LeaderTerm    uint64 `json:"leader_term"`
	Nonce         string `json:"nonce"`
	ExpiresUnix   int64  `json:"expires_unix"`
}

type CapabilityTransferRequest struct {
	Prepare  CapabilityPrepareBody `json:"prepare"`
	CAKeyPEM []byte                `json:"ca_key_pem"`
}

type CapabilityTransferResponse struct {
	Proof []byte `json:"proof"`
}

type CapabilityTransfer func(context.Context, CapabilityTransferRequest) (CapabilityTransferResponse, error)

type CapabilityManager struct {
	Node   *Node
	Dir    string
	NodeID string
	Now    func() time.Time
}

const capabilityPrepareTTL = time.Minute

func (m CapabilityManager) EnsureInitialized() error {
	if m.Node == nil || !m.Node.IsLeader() {
		return errcode.E(errcode.UNAVAILABLE, "control leader required")
	}
	bundle, err := LoadBundle(m.Dir)
	if err != nil {
		return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
	}
	if err := verifyCAKeyMatches(bundle.CACertPEM, bundle.CAKeyPEM); err != nil {
		return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
	}
	fingerprint, err := CASPKIFingerprint(bundle.CACertPEM)
	if err != nil {
		return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
	}
	nodeID := m.NodeID
	if nodeID == "" {
		_, nodeID, err = ParseIDs(bundle.AgentCertPEM)
		if err != nil {
			return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
		}
	}
	serial, err := CertSerial(bundle.AgentCertPEM)
	if err != nil {
		return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
	}
	state := m.Node.View().AdmissionCapability
	if state.CAFingerprint == "" {
		command, err := EncodeCommand(CmdCapabilityInit, CapabilityInitBody{
			CAFingerprint: fingerprint, Epoch: 1, NodeID: nodeID, CertSerial: serial,
		})
		if err != nil {
			return err
		}
		if err := m.Node.Apply(command, admissionApplyTO); err != nil {
			return err
		}
		state = m.Node.View().AdmissionCapability
	}
	return m.CheckReady()
}

func (m CapabilityManager) CheckReady() error {
	if m.Node == nil {
		return errcode.E(errcode.UNAVAILABLE, "control plane unavailable")
	}
	bundle, err := LoadBundle(m.Dir)
	if err != nil {
		return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
	}
	if err := verifyCAKeyMatches(bundle.CACertPEM, bundle.CAKeyPEM); err != nil {
		return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
	}
	fingerprint, err := CASPKIFingerprint(bundle.CACertPEM)
	if err != nil {
		return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
	}
	nodeID := m.NodeID
	if nodeID == "" {
		_, nodeID, err = ParseIDs(bundle.AgentCertPEM)
		if err != nil {
			return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
		}
	}
	serial, err := CertSerial(bundle.AgentCertPEM)
	if err != nil {
		return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
	}
	state := m.Node.View().AdmissionCapability
	if state.CAFingerprint != fingerprint || state.Epoch == 0 {
		return errcode.E(errcode.DEGRADED, "admission capability fingerprint mismatch")
	}
	local, ok := state.Nodes[nodeID]
	if !ok || local.Status != CapabilityReady || local.CertSerial != serial || local.Epoch != state.Epoch {
		return errcode.E(errcode.DEGRADED, "local admission capability not ready")
	}
	return nil
}

func (m CapabilityManager) Promote(ctx context.Context, operationID, targetNodeID string, transfer CapabilityTransfer) error {
	if operationID == "" || targetNodeID == "" {
		return errcode.E(errcode.INVALID, "operation_id and target node required")
	}
	if transfer == nil {
		return errcode.E(errcode.UNAVAILABLE, "capability target unavailable")
	}
	if err := m.EnsureInitialized(); err != nil {
		return err
	}
	state := m.Node.View()
	target, ok := state.Members[targetNodeID]
	if !ok || target.Status != MemberAdmitted || target.CertSerial == "" {
		return errcode.E(errcode.INVALID, "capability target not admitted")
	}

	now := m.now()
	prepare, reuse, err := existingCapabilityPrepare(state.AdmissionCapability, operationID, targetNodeID)
	if err != nil {
		return err
	}
	if ready, ok := state.AdmissionCapability.Nodes[targetNodeID]; ok && ready.Status == CapabilityReady && ready.CertSerial == target.CertSerial && ready.Epoch == state.AdmissionCapability.Epoch {
		reuse = false
	}
	if !reuse {
		nonce := make([]byte, 32)
		if _, err := rand.Read(nonce); err != nil {
			return fmt.Errorf("generate capability nonce: %w", err)
		}
		prepare = CapabilityPrepareBody{
			OperationID: operationID, NodeID: targetNodeID, CertSerial: target.CertSerial,
			CAFingerprint: state.AdmissionCapability.CAFingerprint,
			Epoch:         state.AdmissionCapability.Epoch, LeaderTerm: m.Node.CurrentTerm(),
			Nonce: hex.EncodeToString(nonce), ExpiresUnix: now.Add(capabilityPrepareTTL).Unix(),
		}
		command, err := EncodeCommand(CmdCapabilityPrepare, prepare)
		if err != nil {
			return err
		}
		if err := m.Node.Apply(command, admissionApplyTO); err != nil {
			return err
		}
	}
	if prepare.ExpiresUnix <= now.Unix() {
		return errcode.E(errcode.CONFLICT, "capability prepare expired")
	}
	bundle, err := LoadBundle(m.Dir)
	if err != nil {
		return errcode.Wrap(errcode.DEGRADED, "admission capability unavailable", err)
	}
	response, err := transfer(ctx, CapabilityTransferRequest{Prepare: prepare, CAKeyPEM: bundle.CAKeyPEM})
	if err != nil {
		return err
	}
	challenge := CapabilityChallenge(prepare)
	if err := VerifyCapabilityProof(bundle.CACertPEM, challenge, response.Proof); err != nil {
		return err
	}
	if !m.Node.IsLeader() || m.Node.CurrentTerm() != prepare.LeaderTerm {
		return errcode.E(errcode.CONFLICT, "control leader term changed")
	}
	command, err := EncodeCommand(CmdCapabilityReady, CapabilityReadyBody(prepare))
	if err != nil {
		return err
	}
	return m.Node.Apply(command, admissionApplyTO)
}

func (m CapabilityManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func existingCapabilityPrepare(state AdmissionCapabilityState, operationID, nodeID string) (CapabilityPrepareBody, bool, error) {
	for existingNodeID, node := range state.Nodes {
		if node.OperationID != operationID {
			continue
		}
		if existingNodeID != nodeID {
			return CapabilityPrepareBody{}, false, errcode.E(errcode.CONFLICT, "capability operation already used")
		}
		return CapabilityPrepareBody{
			OperationID: operationID, NodeID: nodeID, CertSerial: node.CertSerial,
			CAFingerprint: state.CAFingerprint, Epoch: node.Epoch, LeaderTerm: node.LeaderTerm,
			Nonce: node.Nonce, ExpiresUnix: node.ExpiresUnix,
		}, true, nil
	}
	return CapabilityPrepareBody{}, false, nil
}

func CASPKIFingerprint(caCertPEM []byte) (string, error) {
	cert, err := parseCertPEM(caCertPEM)
	if err != nil {
		return "", fmt.Errorf("parse CA certificate: %w", err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:]), nil
}

func InstallCAKey(dir string, caKeyPEM []byte) error {
	caKeyInstallMu.Lock()
	defer caKeyInstallMu.Unlock()

	caCertPEM, err := os.ReadFile(filepath.Join(dir, caCertFile))
	if err != nil {
		return fmt.Errorf("read CA certificate: %w", err)
	}
	if err := verifyCAKeyMatches(caCertPEM, caKeyPEM); err != nil {
		return err
	}
	path := filepath.Join(dir, caKeyFile)
	if existing, err := os.ReadFile(path); err == nil {
		if err := verifyCAKeyMatches(caCertPEM, existing); err != nil {
			return errcode.E(errcode.DENIED, "existing CA key does not match cluster CA")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errcode.E(errcode.DENIED, "existing CA key is not a regular file")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("chmod CA key: %w", err)
		}
		return syncInstalledCAKey(path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read existing CA key: %w", err)
	}
	return installCAKeyAtomic(path, caKeyPEM)
}

func installCAKeyAtomic(path string, caKeyPEM []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ca.key-*.tmp")
	if err != nil {
		return fmt.Errorf("create CA key temp file: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod CA key temp file: %w", err)
	}
	if _, err := tmp.Write(caKeyPEM); err != nil {
		return fmt.Errorf("write CA key temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync CA key temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close CA key temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install CA key: %w", err)
	}
	removeTemp = false
	return syncInstalledCAKey(path)
}

func syncInstalledCAKey(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open installed CA key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync installed CA key: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close installed CA key: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open CA key directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync CA key directory: %w", err)
	}
	return nil
}

func SignCapabilityProof(caKeyPEM []byte, challenge CapabilityChallenge) ([]byte, error) {
	key, err := parseECKeyPEM(caKeyPEM)
	if err != nil {
		return nil, errcode.Wrap(errcode.DENIED, "invalid CA key", err)
	}
	digest, err := capabilityChallengeDigest(challenge)
	if err != nil {
		return nil, err
	}
	proof, err := ecdsa.SignASN1(rand.Reader, key, digest)
	if err != nil {
		return nil, fmt.Errorf("sign capability proof: %w", err)
	}
	return proof, nil
}

func VerifyCapabilityProof(caCertPEM []byte, challenge CapabilityChallenge, proof []byte) error {
	cert, err := parseCertPEM(caCertPEM)
	if err != nil {
		return errcode.Wrap(errcode.DENIED, "invalid CA certificate", err)
	}
	publicKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return errcode.E(errcode.DENIED, "CA public key must be ECDSA")
	}
	digest, err := capabilityChallengeDigest(challenge)
	if err != nil {
		return err
	}
	if !ecdsa.VerifyASN1(publicKey, digest, proof) {
		return errcode.E(errcode.DENIED, "capability proof invalid")
	}
	return nil
}

func verifyCAKeyMatches(caCertPEM, caKeyPEM []byte) error {
	cert, err := parseCertPEM(caCertPEM)
	if err != nil {
		return errcode.Wrap(errcode.DENIED, "invalid CA certificate", err)
	}
	key, err := parseECKeyPEM(caKeyPEM)
	if err != nil {
		return errcode.Wrap(errcode.DENIED, "invalid CA key", err)
	}
	want, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal CA certificate key: %w", err)
	}
	got, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal CA private key public part: %w", err)
	}
	if string(want) != string(got) {
		return errcode.E(errcode.DENIED, "CA key does not match cluster CA")
	}
	return nil
}

func capabilityChallengeDigest(challenge CapabilityChallenge) ([]byte, error) {
	raw, err := json.Marshal(challenge)
	if err != nil {
		return nil, fmt.Errorf("marshal capability challenge: %w", err)
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func (s *State) applyCapabilityInit(body CapabilityInitBody, now time.Time) error {
	body.CAFingerprint = strings.ToLower(body.CAFingerprint)
	body.CertSerial = strings.ToUpper(body.CertSerial)
	if !validCAFingerprint(body.CAFingerprint) || body.Epoch == 0 || body.NodeID == "" || body.CertSerial == "" {
		return errcode.E(errcode.INVALID, "capability init fields required")
	}
	member, ok := s.Members[body.NodeID]
	if !ok || member.Status != MemberAdmitted {
		return errcode.E(errcode.INVALID, "capability node not admitted")
	}
	if strings.ToUpper(member.CertSerial) != body.CertSerial {
		return errcode.E(errcode.CONFLICT, "capability certificate changed")
	}
	capability := &s.AdmissionCapability
	if capability.CAFingerprint != "" {
		if capability.CAFingerprint != body.CAFingerprint || capability.Epoch != body.Epoch {
			return errcode.E(errcode.CONFLICT, "capability already initialized")
		}
		if existing, exists := capability.Nodes[body.NodeID]; exists {
			if existing.Status == CapabilityReady && existing.CertSerial == body.CertSerial && existing.Epoch == body.Epoch {
				return nil
			}
			return errcode.E(errcode.CONFLICT, "capability node state conflict")
		}
		return errcode.E(errcode.CONFLICT, "capability already initialized")
	}
	capability.CAFingerprint = body.CAFingerprint
	capability.Epoch = body.Epoch
	capability.Nodes[body.NodeID] = AdmissionCapabilityNode{
		Status: CapabilityReady, CertSerial: body.CertSerial, Epoch: body.Epoch, UpdatedUnix: now.Unix(),
	}
	return nil
}

func (s *State) applyCapabilityPrepare(body CapabilityPrepareBody, now time.Time) error {
	body.CAFingerprint = strings.ToLower(body.CAFingerprint)
	body.CertSerial = strings.ToUpper(body.CertSerial)
	if body.OperationID == "" || body.NodeID == "" || body.CertSerial == "" || body.LeaderTerm == 0 || body.Nonce == "" || body.ExpiresUnix == 0 {
		return errcode.E(errcode.INVALID, "capability prepare fields required")
	}
	if body.ExpiresUnix <= now.Unix() {
		return errcode.E(errcode.INVALID, "capability prepare expired")
	}
	capability := s.AdmissionCapability
	if capability.CAFingerprint == "" || capability.Epoch == 0 {
		return errcode.E(errcode.CONFLICT, "capability not initialized")
	}
	if body.CAFingerprint != capability.CAFingerprint || body.Epoch != capability.Epoch {
		return errcode.E(errcode.CONFLICT, "capability epoch or fingerprint changed")
	}
	member, ok := s.Members[body.NodeID]
	if !ok || member.Status != MemberAdmitted {
		return errcode.E(errcode.INVALID, "capability node not admitted")
	}
	if strings.ToUpper(member.CertSerial) != body.CertSerial {
		return errcode.E(errcode.CONFLICT, "capability certificate changed")
	}
	for nodeID, existing := range capability.Nodes {
		if existing.OperationID == body.OperationID && nodeID != body.NodeID {
			return errcode.E(errcode.CONFLICT, "capability operation already used")
		}
	}
	if existing, exists := capability.Nodes[body.NodeID]; exists && existing.OperationID == body.OperationID {
		if capabilityNodeMatches(existing, body) {
			return nil
		}
		// A READY operation may publish a fresh challenge before retrying AddVoter.
		if existing.Status != CapabilityReady || existing.CertSerial != body.CertSerial || existing.Epoch != body.Epoch {
			return errcode.E(errcode.CONFLICT, "capability operation parameters changed")
		}
	}
	s.AdmissionCapability.Nodes[body.NodeID] = AdmissionCapabilityNode{
		Status: CapabilityPrepared, OperationID: body.OperationID, CertSerial: body.CertSerial,
		LeaderTerm: body.LeaderTerm, Epoch: body.Epoch, Nonce: body.Nonce,
		ExpiresUnix: body.ExpiresUnix, UpdatedUnix: now.Unix(),
	}
	return nil
}

func (s *State) applyCapabilityReady(body CapabilityReadyBody, now time.Time) error {
	prepare := CapabilityPrepareBody(body)
	prepare.CAFingerprint = strings.ToLower(prepare.CAFingerprint)
	prepare.CertSerial = strings.ToUpper(prepare.CertSerial)
	if prepare.OperationID == "" || prepare.NodeID == "" {
		return errcode.E(errcode.INVALID, "capability ready fields required")
	}
	capability := s.AdmissionCapability
	if prepare.CAFingerprint != capability.CAFingerprint || prepare.Epoch != capability.Epoch {
		return errcode.E(errcode.CONFLICT, "capability epoch or fingerprint changed")
	}
	existing, ok := capability.Nodes[prepare.NodeID]
	if !ok || !capabilityNodeMatches(existing, prepare) {
		return errcode.E(errcode.CONFLICT, "capability prepare changed")
	}
	if existing.Status == CapabilityReady {
		return nil
	}
	if existing.Status != CapabilityPrepared {
		return errcode.E(errcode.CONFLICT, "capability node not prepared")
	}
	if prepare.ExpiresUnix <= now.Unix() {
		return errcode.E(errcode.INVALID, "capability prepare expired")
	}
	existing.Status = CapabilityReady
	existing.UpdatedUnix = now.Unix()
	s.AdmissionCapability.Nodes[prepare.NodeID] = existing
	return nil
}

func capabilityNodeMatches(node AdmissionCapabilityNode, body CapabilityPrepareBody) bool {
	return node.OperationID == body.OperationID &&
		node.CertSerial == body.CertSerial &&
		node.LeaderTerm == body.LeaderTerm &&
		node.Epoch == body.Epoch &&
		node.Nonce == body.Nonce &&
		node.ExpiresUnix == body.ExpiresUnix
}

func validCAFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
