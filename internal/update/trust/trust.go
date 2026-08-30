// Package trust verifies signed ProcMesh release metadata.
package trust

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

const (
	SchemaVersion      = 1
	OfficialRepository = "xiaoyannzbbb/procmesh"
	OfficialOrigin     = "https://github.com"
	maxArtifactSize    = 512 << 20
)

var (
	versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	hexPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	keyIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// ChannelIndex identifies the signed manifest for the current stable release.
type ChannelIndex struct {
	SchemaVersion int            `json:"schema_version"`
	Channel       string         `json:"channel"`
	GeneratedAt   string         `json:"generated_at"`
	ExpiresAt     string         `json:"expires_at"`
	Release       ChannelRelease `json:"release"`
}

type ChannelRelease struct {
	Version              string `json:"version"`
	ManifestURL          string `json:"manifest_url"`
	ManifestSHA256       string `json:"manifest_sha256"`
	ManifestSignatureURL string `json:"manifest_signature_url"`
}

// Manifest describes all artifacts and compatibility promises for one release.
type Manifest struct {
	SchemaVersion           int        `json:"schema_version"`
	ReleaseVersion          string     `json:"release_version"`
	Channel                 string     `json:"channel"`
	PublishedAt             string     `json:"published_at"`
	ProtocolVersion         int        `json:"protocol_version"`
	CompatibleFromProtocols []int      `json:"compatible_from_protocols"`
	RollbackSafeFrom        []string   `json:"rollback_safe_from"`
	ShimProtocolMin         int        `json:"shim_protocol_min"`
	ShimProtocolMax         int        `json:"shim_protocol_max"`
	Artifacts               []Artifact `json:"artifacts"`
	ReleaseNotesURL         string     `json:"release_notes_url"`
}

type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Signature is a detached signature envelope. KeyID permits planned key overlap.
type Signature struct {
	SchemaVersion int    `json:"schema_version"`
	KeyID         string `json:"key_id"`
	Algorithm     string `json:"algorithm"`
	Signature     string `json:"signature"`
}

type Keyring map[string]ed25519.PublicKey

type KeyRegistry struct {
	SchemaVersion int             `json:"schema_version"`
	Keys          []RegisteredKey `json:"keys"`
}

type RegisteredKey struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
}

type Verifier struct {
	Keys Keyring
	Now  func() time.Time
}

func CanonicalJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

func CanonicalKeyRegistry(keys map[string]ed25519.PublicKey) ([]byte, error) {
	keyIDs := make([]string, 0, len(keys))
	for keyID := range keys {
		keyIDs = append(keyIDs, keyID)
	}
	slices.Sort(keyIDs)
	registry := KeyRegistry{SchemaVersion: SchemaVersion, Keys: make([]RegisteredKey, 0, len(keyIDs))}
	for _, keyID := range keyIDs {
		publicKey := keys[keyID]
		if !keyIDPattern.MatchString(keyID) || len(publicKey) != ed25519.PublicKeySize {
			return nil, invalidMetadata(errors.New("invalid trusted key"))
		}
		registry.Keys = append(registry.Keys, RegisteredKey{
			KeyID: keyID, Algorithm: "ed25519", PublicKey: base64.StdEncoding.EncodeToString(publicKey),
		})
	}
	return CanonicalJSON(registry)
}

func ParseKeyRegistry(payload []byte) (Keyring, error) {
	var registry KeyRegistry
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return nil, invalidMetadata(err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, invalidMetadata(err)
	}
	if registry.SchemaVersion != SchemaVersion {
		return nil, invalidMetadata(errors.New("unsupported key registry"))
	}
	keys := make(Keyring, len(registry.Keys))
	for _, registered := range registry.Keys {
		if !keyIDPattern.MatchString(registered.KeyID) || registered.Algorithm != "ed25519" {
			return nil, invalidMetadata(errors.New("invalid trusted key"))
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(registered.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, invalidMetadata(errors.New("invalid trusted public key"))
		}
		if _, duplicate := keys[registered.KeyID]; duplicate {
			return nil, invalidMetadata(errors.New("duplicate trusted key"))
		}
		keys[registered.KeyID] = ed25519.PublicKey(decoded)
	}
	return keys, nil
}

func (v Verifier) VerifyChannel(payload, detachedSignature []byte) (ChannelIndex, error) {
	var index ChannelIndex
	if err := verifySignedJSON(v.Keys, payload, detachedSignature, &index); err != nil {
		return ChannelIndex{}, err
	}
	if err := validateIndex(index, v.now()); err != nil {
		return ChannelIndex{}, invalidMetadata(err)
	}
	return index, nil
}

func (v Verifier) VerifyManifest(index ChannelIndex, payload, detachedSignature []byte) (Manifest, error) {
	expected, err := hex.DecodeString(index.Release.ManifestSHA256)
	if err != nil {
		return Manifest{}, invalidMetadata(err)
	}
	actual := sha256.Sum256(payload)
	if subtle.ConstantTimeCompare(expected, actual[:]) != 1 {
		return Manifest{}, invalidMetadata(errors.New("manifest digest mismatch"))
	}

	var manifest Manifest
	if err := verifySignedJSON(v.Keys, payload, detachedSignature, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := validateManifest(manifest, index.Release.Version); err != nil {
		return Manifest{}, invalidMetadata(err)
	}
	return manifest, nil
}

func (v Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now().UTC()
	}
	return time.Now().UTC()
}

func verifySignedJSON(keys Keyring, payload, detachedSignature []byte, destination any) error {
	if len(payload) == 0 || len(detachedSignature) == 0 {
		return invalidMetadata(errors.New("signed metadata required"))
	}
	if err := decodeCanonical(payload, destination); err != nil {
		return invalidMetadata(err)
	}

	var signature Signature
	if err := decodeCanonical(detachedSignature, &signature); err != nil {
		return invalidMetadata(err)
	}
	if signature.SchemaVersion != SchemaVersion || signature.Algorithm != "ed25519" || !keyIDPattern.MatchString(signature.KeyID) {
		return invalidMetadata(errors.New("unsupported signature envelope"))
	}
	publicKey, ok := keys[signature.KeyID]
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return invalidMetadata(errors.New("untrusted signing key"))
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(signature.Signature)
	if err != nil || len(decoded) != ed25519.SignatureSize || !ed25519.Verify(publicKey, payload, decoded) {
		return invalidMetadata(errors.New("signature verification failed"))
	}
	return nil
}

func decodeCanonical(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	canonical, err := CanonicalJSON(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, canonical) {
		return errors.New("metadata is not canonical JSON")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateIndex(index ChannelIndex, now time.Time) error {
	if index.SchemaVersion != SchemaVersion || index.Channel != "stable" || !versionPattern.MatchString(index.Release.Version) {
		return errors.New("unsupported channel metadata")
	}
	generatedAt, err := parseCanonicalTime(index.GeneratedAt)
	if err != nil {
		return err
	}
	expiresAt, err := parseCanonicalTime(index.ExpiresAt)
	if err != nil {
		return err
	}
	if !expiresAt.After(generatedAt) || now.Before(generatedAt) || !now.Before(expiresAt) {
		return errors.New("channel metadata is outside its validity period")
	}
	if !hexPattern.MatchString(index.Release.ManifestSHA256) {
		return errors.New("invalid manifest digest")
	}
	base := OfficialOrigin + "/" + OfficialRepository + "/releases/download/" + index.Release.Version + "/"
	if index.Release.ManifestURL != base+"manifest.json" || index.Release.ManifestSignatureURL != base+"manifest.json.sig" {
		return errors.New("non-official manifest URL")
	}
	return nil
}

func validateManifest(manifest Manifest, expectedVersion string) error {
	if manifest.SchemaVersion != SchemaVersion || manifest.Channel != "stable" || manifest.ReleaseVersion != expectedVersion || !versionPattern.MatchString(manifest.ReleaseVersion) {
		return errors.New("manifest identity mismatch")
	}
	if _, err := parseCanonicalTime(manifest.PublishedAt); err != nil {
		return err
	}
	if manifest.ProtocolVersion <= 0 || manifest.ShimProtocolMin <= 0 || manifest.ShimProtocolMax < manifest.ShimProtocolMin {
		return errors.New("invalid protocol compatibility")
	}
	if len(manifest.CompatibleFromProtocols) == 0 || len(manifest.RollbackSafeFrom) == 0 || len(manifest.Artifacts) == 0 {
		return errors.New("incomplete compatibility metadata")
	}
	for _, protocol := range manifest.CompatibleFromProtocols {
		if protocol <= 0 {
			return errors.New("invalid compatible protocol")
		}
	}
	for _, version := range manifest.RollbackSafeFrom {
		if !versionPattern.MatchString(version) || version == manifest.ReleaseVersion {
			return errors.New("invalid rollback version")
		}
	}
	notesURL := OfficialOrigin + "/" + OfficialRepository + "/releases/tag/" + manifest.ReleaseVersion
	if manifest.ReleaseNotesURL != notesURL {
		return errors.New("non-official release notes URL")
	}

	seen := make(map[string]struct{}, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if err := validateArtifact(artifact, manifest.ReleaseVersion); err != nil {
			return err
		}
		target := artifact.OS + "/" + artifact.Arch
		if _, duplicate := seen[target]; duplicate {
			return errors.New("duplicate artifact target")
		}
		seen[target] = struct{}{}
	}
	return nil
}

func validateArtifact(artifact Artifact, version string) error {
	validTarget := false
	switch artifact.OS + "/" + artifact.Arch {
	case "linux/amd64", "linux/arm64", "linux/armv7", "darwin/amd64", "darwin/arm64":
		validTarget = true
	}
	if !validTarget || artifact.Size <= 0 || artifact.Size > maxArtifactSize || !hexPattern.MatchString(artifact.SHA256) {
		return errors.New("invalid artifact metadata")
	}
	parsed, err := url.Parse(artifact.URL)
	if err != nil || parsed.IsAbs() || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(artifact.URL, "\\") || path.Base(artifact.URL) != artifact.URL {
		return errors.New("unsafe artifact URL")
	}
	expected := fmt.Sprintf("procmesh_%s_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), artifact.OS, artifact.Arch)
	if artifact.URL != expected {
		return errors.New("artifact filename mismatch")
	}
	return nil
}

func parseCanonicalTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Format(time.RFC3339) != value || !strings.HasSuffix(value, "Z") {
		return time.Time{}, errors.New("timestamp must be canonical UTC RFC3339")
	}
	return parsed, nil
}

func invalidMetadata(cause error) error {
	return errcode.Wrap(errcode.INVALID, "release metadata verification failed", cause)
}
