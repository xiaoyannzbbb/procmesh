// Package releasemeta creates canonical, signed release metadata.
package releasemeta

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/update/trust"
)

var artifactPattern = regexp.MustCompile(`^procmesh_([0-9]+\.[0-9]+\.[0-9]+)_(linux|darwin)_(amd64|arm64|armv7)\.tar\.gz$`)

type Options struct {
	Version          string
	ArtifactDir      string
	RollbackSafeFrom []string
	ProtocolVersion  int
	ShimProtocolMin  int
	ShimProtocolMax  int
	KeyID            string
	PrivateKey       ed25519.PrivateKey
	TrustedKeys      trust.Keyring
	Now              time.Time
	IndexValidity    time.Duration
}

type Files struct {
	Manifest          []byte
	ManifestSignature []byte
	ChannelIndex      []byte
	ChannelSignature  []byte
}

func Generate(options Options) (Files, error) {
	if err := validateSigningKey(options); err != nil {
		return Files{}, err
	}
	if options.Now.IsZero() || options.Now.Location() != time.UTC || options.IndexValidity <= 0 {
		return Files{}, errors.New("UTC generation time and positive index validity required")
	}

	artifacts, err := readArtifacts(options.ArtifactDir, options.Version)
	if err != nil {
		return Files{}, err
	}
	manifest := trust.Manifest{
		SchemaVersion:           trust.SchemaVersion,
		ReleaseVersion:          options.Version,
		Channel:                 "stable",
		PublishedAt:             options.Now.Format(time.RFC3339),
		ProtocolVersion:         options.ProtocolVersion,
		CompatibleFromProtocols: []int{options.ProtocolVersion},
		RollbackSafeFrom:        append([]string(nil), options.RollbackSafeFrom...),
		ShimProtocolMin:         options.ShimProtocolMin,
		ShimProtocolMax:         options.ShimProtocolMax,
		Artifacts:               artifacts,
		ReleaseNotesURL:         trust.OfficialOrigin + "/" + trust.OfficialRepository + "/releases/tag/" + options.Version,
	}
	manifestBytes, err := trust.CanonicalJSON(manifest)
	if err != nil {
		return Files{}, err
	}
	manifestSignature, err := sign(options.KeyID, options.PrivateKey, manifestBytes)
	if err != nil {
		return Files{}, err
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	releaseBase := trust.OfficialOrigin + "/" + trust.OfficialRepository + "/releases/download/" + options.Version + "/"
	index := trust.ChannelIndex{
		SchemaVersion: trust.SchemaVersion,
		Channel:       "stable",
		GeneratedAt:   options.Now.Format(time.RFC3339),
		ExpiresAt:     options.Now.Add(options.IndexValidity).Format(time.RFC3339),
		Release: trust.ChannelRelease{
			Version:              options.Version,
			ManifestURL:          releaseBase + "manifest.json",
			ManifestSHA256:       hex.EncodeToString(manifestDigest[:]),
			ManifestSignatureURL: releaseBase + "manifest.json.sig",
		},
	}
	indexBytes, err := trust.CanonicalJSON(index)
	if err != nil {
		return Files{}, err
	}
	indexSignature, err := sign(options.KeyID, options.PrivateKey, indexBytes)
	if err != nil {
		return Files{}, err
	}
	verifier := trust.Verifier{Keys: options.TrustedKeys, Now: func() time.Time { return options.Now }}
	verifiedIndex, err := verifier.VerifyChannel(indexBytes, indexSignature)
	if err != nil {
		return Files{}, fmt.Errorf("self-verify channel metadata: %w", err)
	}
	if _, err := verifier.VerifyManifest(verifiedIndex, manifestBytes, manifestSignature); err != nil {
		return Files{}, fmt.Errorf("self-verify release manifest: %w", err)
	}
	return Files{
		Manifest: manifestBytes, ManifestSignature: manifestSignature,
		ChannelIndex: indexBytes, ChannelSignature: indexSignature,
	}, nil
}

func validateSigningKey(options Options) error {
	if len(options.PrivateKey) != ed25519.PrivateKeySize {
		return errors.New("valid Ed25519 private key required")
	}
	trusted, ok := options.TrustedKeys[options.KeyID]
	if !ok || !bytes.Equal(trusted, options.PrivateKey.Public().(ed25519.PublicKey)) {
		return errors.New("signing key is not present in the embedded trusted key registry")
	}
	return nil
}

func readArtifacts(directory, version string) ([]trust.Artifact, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read artifact directory: %w", err)
	}
	artifacts := make([]trust.Artifact, 0, len(entries))
	versionWithoutPrefix := strings.TrimPrefix(version, "v")
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := artifactPattern.FindStringSubmatch(entry.Name())
		if matches == nil || matches[1] != versionWithoutPrefix {
			continue
		}
		if matches[2] == "darwin" && matches[4] == "armv7" {
			return nil, fmt.Errorf("unsupported artifact target %s", entry.Name())
		}
		artifactPath := filepath.Join(directory, entry.Name())
		digest, size, err := hashFile(artifactPath)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, trust.Artifact{
			OS: matches[2], Arch: matches[3], URL: entry.Name(), Size: size, SHA256: digest,
		})
	}
	if len(artifacts) == 0 {
		return nil, errors.New("no release archives found")
	}
	slices.SortFunc(artifacts, func(a, b trust.Artifact) int {
		return strings.Compare(a.OS+"/"+a.Arch, b.OS+"/"+b.Arch)
	})
	return artifacts, nil
}

func hashFile(name string) (string, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func sign(keyID string, privateKey ed25519.PrivateKey, payload []byte) ([]byte, error) {
	return trust.CanonicalJSON(trust.Signature{
		SchemaVersion: trust.SchemaVersion,
		KeyID:         keyID,
		Algorithm:     "ed25519",
		Signature:     base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
	})
}
