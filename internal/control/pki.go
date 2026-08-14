package control

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

const URIPrefix = "procmesh://"

const (
	caCertFile    = "ca.crt"
	caKeyFile     = "ca.key"
	agentCertFile = "agent.crt"
	agentKeyFile  = "agent.key"

	caValidity    = 10 * 365 * 24 * time.Hour
	agentValidity = 2 * 365 * 24 * time.Hour
)

// Bundle holds PEM-encoded Cluster CA and Agent credentials.
type Bundle struct {
	CACertPEM    []byte
	CAKeyPEM     []byte
	AgentCertPEM []byte
	AgentKeyPEM  []byte
}

// AgentURI returns procmesh://<clusterID>/<nodeID>.
func AgentURI(clusterID, nodeID string) string {
	return URIPrefix + clusterID + "/" + nodeID
}

// ParseAgentURI extracts cluster and node IDs from a procmesh URI SAN.
func ParseAgentURI(uri string) (clusterID, nodeID string, err error) {
	if !strings.HasPrefix(uri, URIPrefix) {
		return "", "", errcode.E(errcode.INVALID, "agent uri must use procmesh scheme")
	}
	rest := strings.TrimPrefix(uri, URIPrefix)
	// Allow only one path separator between cluster and node.
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i == len(rest)-1 {
		return "", "", errcode.E(errcode.INVALID, "agent uri must be procmesh://<cluster>/<node>")
	}
	if strings.Contains(rest[i+1:], "/") {
		return "", "", errcode.E(errcode.INVALID, "agent uri must be procmesh://<cluster>/<node>")
	}
	return rest[:i], rest[i+1:], nil
}

// NewBundle creates a self-signed Cluster CA and an Agent cert for this node.
func NewBundle(clusterID, nodeID string, now time.Time) (Bundle, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Bundle{}, fmt.Errorf("generate ca key: %w", err)
	}
	caCert, err := createCACert(clusterID, caKey, now)
	if err != nil {
		return Bundle{}, err
	}
	caCertPEM, err := encodeCertPEM(caCert)
	if err != nil {
		return Bundle{}, err
	}
	caKeyPEM, err := encodeECKeyPEM(caKey)
	if err != nil {
		return Bundle{}, err
	}

	agentKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Bundle{}, fmt.Errorf("generate agent key: %w", err)
	}
	agentCert, err := createAgentCert(caCert, caKey, &agentKey.PublicKey, clusterID, nodeID, now)
	if err != nil {
		return Bundle{}, err
	}
	agentCertPEM, err := encodeCertPEM(agentCert)
	if err != nil {
		return Bundle{}, err
	}
	agentKeyPEM, err := encodeECKeyPEM(agentKey)
	if err != nil {
		return Bundle{}, err
	}

	return Bundle{
		CACertPEM:    caCertPEM,
		CAKeyPEM:     caKeyPEM,
		AgentCertPEM: agentCertPEM,
		AgentKeyPEM:  agentKeyPEM,
	}, nil
}

// NewCSR creates a CSR with URI SAN procmesh://clusterID/nodeID and a new P-256 key.
func NewCSR(clusterID, nodeID string) (csrPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate csr key: %w", err)
	}
	uri, err := url.Parse(AgentURI(clusterID, nodeID))
	if err != nil {
		return nil, nil, fmt.Errorf("parse agent uri: %w", err)
	}
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "node-" + nodeID},
		URIs:    []*url.URL{uri},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create csr: %w", err)
	}
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	keyPEM, err = encodeECKeyPEM(key)
	if err != nil {
		return nil, nil, err
	}
	return csrPEM, keyPEM, nil
}

// SignCSR signs a CSR with the Cluster CA. Only CSR node_id must match nodeID;
// the issued cert URI is rewritten to procmesh://clusterID/nodeID.
func SignCSR(caCertPEM, caKeyPEM, csrPEM []byte, clusterID, nodeID string, now time.Time) (certPEM []byte, err error) {
	caCert, err := parseCertPEM(caCertPEM)
	if err != nil {
		return nil, err
	}
	caKey, err := parseECKeyPEM(caKeyPEM)
	if err != nil {
		return nil, err
	}
	csr, err := parseCSRPEM(csrPEM)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, errcode.E(errcode.INVALID, "csr signature invalid")
	}
	csrCID, csrNID, err := idsFromURIs(csr.URIs)
	if err != nil {
		return nil, errcode.E(errcode.INVALID, "csr missing procmesh uri san")
	}
	_ = csrCID // cluster segment may be a placeholder (e.g. "join")
	if csrNID != nodeID {
		return nil, errcode.E(errcode.INVALID, "csr node_id does not match")
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errcode.E(errcode.INVALID, "csr public key must be ecdsa")
	}
	cert, err := createAgentCert(caCert, caKey, pub, clusterID, nodeID, now)
	if err != nil {
		return nil, err
	}
	return encodeCertPEM(cert)
}

// ParseIDs reads cluster and node IDs from an agent cert URI SAN.
func ParseIDs(certPEM []byte) (clusterID, nodeID string, err error) {
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return "", "", err
	}
	return idsFromURIs(cert.URIs)
}

// VerifyAgent checks CA signature, validity window, and URI SAN identity.
// Failures return errcode.DENIED.
func VerifyAgent(caCertPEM, agentCertPEM []byte, clusterID, nodeID string, now time.Time) error {
	caCert, err := parseCertPEM(caCertPEM)
	if err != nil {
		return errcode.E(errcode.DENIED, "invalid ca cert")
	}
	agentCert, err := parseCertPEM(agentCertPEM)
	if err != nil {
		return errcode.E(errcode.DENIED, "invalid agent cert")
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	opts := x509.VerifyOptions{
		Roots:       roots,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		CurrentTime: now,
	}
	if _, err := agentCert.Verify(opts); err != nil {
		return errcode.E(errcode.DENIED, "agent cert verify failed")
	}
	if now.Before(agentCert.NotBefore) || now.After(agentCert.NotAfter) {
		return errcode.E(errcode.DENIED, "agent cert not valid at now")
	}
	cid, nid, err := idsFromURIs(agentCert.URIs)
	if err != nil {
		return errcode.E(errcode.DENIED, "agent cert missing procmesh uri")
	}
	if cid != clusterID || nid != nodeID {
		return errcode.E(errcode.DENIED, "agent identity mismatch")
	}
	return nil
}

// WriteBundle writes CA/agent PEMs under dir with key mode 0600 and cert mode 0640.
func WriteBundle(dir string, b Bundle) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir cluster pki: %w", err)
	}
	files := []struct {
		name string
		data []byte
		perm os.FileMode
	}{
		{caCertFile, b.CACertPEM, 0o640},
		{caKeyFile, b.CAKeyPEM, 0o600},
		{agentCertFile, b.AgentCertPEM, 0o640},
		{agentKeyFile, b.AgentKeyPEM, 0o600},
	}
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, f.data, f.perm); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
		// WriteFile is umask-sensitive; force exact perms.
		if err := os.Chmod(path, f.perm); err != nil {
			return fmt.Errorf("chmod %s: %w", f.name, err)
		}
	}
	return nil
}

// LoadBundle reads CA/agent PEMs from dir.
func LoadBundle(dir string) (Bundle, error) {
	read := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		return b, nil
	}
	caCert, err := read(caCertFile)
	if err != nil {
		return Bundle{}, err
	}
	caKey, err := read(caKeyFile)
	if err != nil {
		return Bundle{}, err
	}
	agentCert, err := read(agentCertFile)
	if err != nil {
		return Bundle{}, err
	}
	agentKey, err := read(agentKeyFile)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		CACertPEM:    caCert,
		CAKeyPEM:     caKey,
		AgentCertPEM: agentCert,
		AgentKeyPEM:  agentKey,
	}, nil
}

func createCACert(clusterID string, key *ecdsa.PrivateKey, now time.Time) (*x509.Certificate, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "procmesh-cluster-" + clusterID},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create ca cert: %w", err)
	}
	return x509.ParseCertificate(der)
}

func createAgentCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, pub *ecdsa.PublicKey, clusterID, nodeID string, now time.Time) (*x509.Certificate, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	uri, err := url.Parse(AgentURI(clusterID, nodeID))
	if err != nil {
		return nil, fmt.Errorf("parse agent uri: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "node-" + nodeID},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(agentValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, pub, caKey)
	if err != nil {
		return nil, fmt.Errorf("create agent cert: %w", err)
	}
	return x509.ParseCertificate(der)
}

func idsFromURIs(uris []*url.URL) (clusterID, nodeID string, err error) {
	for _, u := range uris {
		if u == nil {
			continue
		}
		// url.URL.String() preserves scheme://host/path; for procmesh://cid/nid
		// Host is cid and Path is /nid.
		s := u.String()
		cid, nid, err := ParseAgentURI(s)
		if err == nil {
			return cid, nid, nil
		}
	}
	return "", "", errcode.E(errcode.INVALID, "missing procmesh uri san")
}

func randomSerial() (*big.Int, error) {
	// 128-bit positive serial.
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}
	if n.Sign() == 0 {
		n = big.NewInt(1)
	}
	return n, nil
}

func encodeCertPEM(cert *x509.Certificate) ([]byte, error) {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), nil
}

func encodeECKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal ec key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func parseCertPEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errcode.E(errcode.INVALID, "invalid certificate pem")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseECKeyPEM(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errcode.E(errcode.INVALID, "invalid private key pem")
	}
	switch block.Type {
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		ec, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errcode.E(errcode.INVALID, "private key is not ecdsa")
		}
		return ec, nil
	default:
		return nil, errcode.E(errcode.INVALID, "unsupported private key pem type")
	}
}

func parseCSRPEM(pemBytes []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || (block.Type != "CERTIFICATE REQUEST" && block.Type != "NEW CERTIFICATE REQUEST") {
		return nil, errcode.E(errcode.INVALID, "invalid csr pem")
	}
	return x509.ParseCertificateRequest(block.Bytes)
}
