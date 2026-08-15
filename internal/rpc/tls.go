package rpc

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

// ServerTLS builds a server-side mTLS config.
// ClientAuth requires a cert signed by the cluster CA; VerifyPeerCertificate
// checks the peer URI SAN cluster_id (Agent certs have no DNS SAN).
// revoked, if non-nil, is called with the peer leaf serial (uppercase hex);
// nil is treated as never-revoked.
func ServerTLS(creds control.AgentCreds, clusterID string, revoked func(serial string) bool) (*tls.Config, error) {
	cert, pool, err := loadCreds(creds)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyPeer(rawCerts, pool, clusterID, "", revoked)
		},
	}, nil
}

// ClientTLS builds a client-side mTLS config.
// InsecureSkipVerify is required because Agent certs have URI SAN only (no DNS SAN);
// VerifyPeerCertificate performs CA + identity checks instead.
func ClientTLS(creds control.AgentCreds, clusterID, expectNodeID string, revoked func(serial string) bool) (*tls.Config, error) {
	cert, pool, err := loadCreds(creds)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            pool,
		InsecureSkipVerify: true, //nolint:gosec // hostname N/A; VerifyPeerCertificate enforces identity
		MinVersion:         tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyPeer(rawCerts, pool, clusterID, expectNodeID, revoked)
		},
	}, nil
}

// PeerIdentity reads cluster_id and node_id from the peer leaf cert URI SAN.
func PeerIdentity(state tls.ConnectionState) (clusterID, nodeID string, err error) {
	if len(state.PeerCertificates) == 0 {
		return "", "", errcode.E(errcode.DENIED, "no peer certificate")
	}
	cid, nid, err := idsFromCert(state.PeerCertificates[0])
	if err != nil {
		return "", "", err
	}
	return cid, nid, nil
}

func loadCreds(creds control.AgentCreds) (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.X509KeyPair(creds.AgentCertPEM, creds.AgentKeyPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(creds.CACertPEM) {
		return tls.Certificate{}, nil, errcode.E(errcode.DENIED, "invalid ca cert")
	}
	return cert, pool, nil
}

func verifyPeer(rawCerts [][]byte, roots *x509.CertPool, clusterID, expectNodeID string, revoked func(string) bool) error {
	if len(rawCerts) == 0 {
		return errcode.E(errcode.DENIED, "no peer certificate")
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return errcode.E(errcode.DENIED, "invalid peer certificate")
	}
	intermediates := x509.NewCertPool()
	for _, raw := range rawCerts[1:] {
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return errcode.E(errcode.DENIED, "invalid intermediate certificate")
		}
		intermediates.AddCert(c)
	}
	now := time.Now()
	opts := x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		CurrentTime:   now,
	}
	if _, err := leaf.Verify(opts); err != nil {
		return errcode.E(errcode.DENIED, "peer cert verify failed")
	}
	serial := strings.ToUpper(leaf.SerialNumber.Text(16))
	if revoked != nil && revoked(serial) {
		return errcode.E(errcode.DENIED, "certificate revoked")
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return errcode.E(errcode.DENIED, "peer cert not valid at now")
	}
	cid, nid, err := idsFromCert(leaf)
	if err != nil {
		return err
	}
	if cid != clusterID {
		return errcode.E(errcode.DENIED, "peer cluster identity mismatch")
	}
	if expectNodeID != "" && nid != expectNodeID {
		return errcode.E(errcode.DENIED, "peer node identity mismatch")
	}
	return nil
}

func idsFromCert(cert *x509.Certificate) (clusterID, nodeID string, err error) {
	// Prefer public control helpers when we have PEM; here we already have a parsed cert.
	for _, u := range cert.URIs {
		if u == nil {
			continue
		}
		cid, nid, err := control.ParseAgentURI(u.String())
		if err == nil {
			return cid, nid, nil
		}
	}
	// Fallback: re-encode and use ParseIDs (same source of truth as join path).
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	cid, nid, err := control.ParseIDs(pemBytes)
	if err != nil {
		return "", "", errcode.E(errcode.DENIED, "peer cert missing procmesh uri")
	}
	return cid, nid, nil
}
