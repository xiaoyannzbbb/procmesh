package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
)

const capabilityPromotePath = "/internal/capability/promote"

func (r *rpcRuntime) capabilityPromote(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	peerClusterID, peerNodeID, peerSerial, ok := capabilityPeer(request)
	if !ok || peerClusterID != r.clusterID {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var transfer control.CapabilityTransferRequest
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
	if err := decoder.Decode(&transfer); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	node := r.control()
	if node == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	membership, err := node.RaftMembershipView()
	if err != nil || !membership.HasQuorum || membership.LeaderID != peerNodeID || node.CurrentTerm() != transfer.Prepare.LeaderTerm {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	state := node.View()
	peer, ok := state.Members[peerNodeID]
	if !ok || peer.Status != control.MemberAdmitted || !strings.EqualFold(peer.CertSerial, peerSerial) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !r.waitLocalPrepare(request, transfer.Prepare) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	membership, err = node.RaftMembershipView()
	if err != nil || !membership.HasQuorum || membership.LeaderID != peerNodeID || node.CurrentTerm() != transfer.Prepare.LeaderTerm {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	state = node.View()
	peer, ok = state.Members[peerNodeID]
	if !ok || peer.Status != control.MemberAdmitted || !strings.EqualFold(peer.CertSerial, peerSerial) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := control.InstallCAKey(r.dir, transfer.CAKeyPEM); err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	key, err := os.ReadFile(filepath.Join(r.dir, "ca.key"))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	proof, err := control.SignCapabilityProof(key, control.CapabilityChallenge(transfer.Prepare))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(control.CapabilityTransferResponse{Proof: proof})
}

func (r *rpcRuntime) waitLocalPrepare(request *http.Request, prepare control.CapabilityPrepareBody) bool {
	deadline := time.Now().Add(5 * time.Second)
	if expires := time.Unix(prepare.ExpiresUnix, 0); expires.Before(deadline) {
		deadline = expires
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		node := r.control()
		if node != nil && r.validLocalPrepare(node.View(), prepare) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-request.Context().Done():
			return false
		case <-ticker.C:
		}
	}
}

func (r *rpcRuntime) validLocalPrepare(state control.State, prepare control.CapabilityPrepareBody) bool {
	if prepare.NodeID != r.nodeID || prepare.OperationID == "" || prepare.Nonce == "" {
		return false
	}
	if prepare.ExpiresUnix <= time.Now().Unix() {
		return false
	}
	if prepare.CAFingerprint != state.AdmissionCapability.CAFingerprint || prepare.Epoch != state.AdmissionCapability.Epoch {
		return false
	}
	candidate, ok := state.AdmissionCapability.Nodes[r.nodeID]
	if !ok || candidate.Status != control.CapabilityPrepared {
		return false
	}
	if candidate.OperationID != prepare.OperationID || candidate.CertSerial != strings.ToUpper(prepare.CertSerial) || candidate.LeaderTerm != prepare.LeaderTerm || candidate.Epoch != prepare.Epoch || candidate.Nonce != prepare.Nonce || candidate.ExpiresUnix != prepare.ExpiresUnix {
		return false
	}
	creds, err := control.LoadAgentCreds(r.dir)
	if err != nil {
		return false
	}
	serial, err := control.CertSerial(creds.AgentCertPEM)
	if err != nil || !strings.EqualFold(serial, prepare.CertSerial) {
		return false
	}
	fingerprint, err := control.CASPKIFingerprint(creds.CACertPEM)
	return err == nil && fingerprint == prepare.CAFingerprint
}

func capabilityPeer(request *http.Request) (clusterID, nodeID, serial string, ok bool) {
	if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
		return "", "", "", false
	}
	certificate := request.TLS.PeerCertificates[0]
	for _, uri := range certificate.URIs {
		cid, nid, err := control.ParseAgentURI(uri.String())
		if err == nil {
			return cid, nid, strings.ToUpper(certificate.SerialNumber.Text(16)), true
		}
	}
	return "", "", "", false
}
