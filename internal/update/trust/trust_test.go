package trust_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/update/trust"
)

var (
	keyOne = ed25519.NewKeyFromSeed([]byte("procmesh release test key one!!!"))
	keyTwo = ed25519.NewKeyFromSeed([]byte("procmesh release test key two!!!"))
)

func TestVerifierAcceptsSignedOfficialRelease(t *testing.T) {
	manifestBytes := canonicalManifest(t)
	manifestDigest := sha256.Sum256(manifestBytes)
	indexBytes := canonicalIndex(t, hex.EncodeToString(manifestDigest[:]))
	verifier := testVerifier()

	index, err := verifier.VerifyChannel(indexBytes, sign(t, "2026-key-1", keyOne, indexBytes))
	if err != nil {
		t.Fatalf("VerifyChannel() error = %v", err)
	}
	manifest, err := verifier.VerifyManifest(index, manifestBytes, sign(t, "2026-key-2", keyTwo, manifestBytes))
	if err != nil {
		t.Fatalf("VerifyManifest() error = %v", err)
	}
	if manifest.ReleaseVersion != "v1.2.1" || len(manifest.Artifacts) != 1 {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestVerifierRejectsUntrustedOrMalformedMetadata(t *testing.T) {
	manifestBytes := canonicalManifest(t)
	manifestDigest := sha256.Sum256(manifestBytes)
	validIndex := canonicalIndex(t, hex.EncodeToString(manifestDigest[:]))

	tests := []struct {
		name      string
		index     []byte
		signature []byte
	}{
		{
			name:      "unknown signing key",
			index:     validIndex,
			signature: sign(t, "untrusted-key", keyOne, validIndex),
		},
		{
			name:      "signature does not match payload",
			index:     validIndex,
			signature: sign(t, "2026-key-1", keyTwo, validIndex),
		},
		{
			name:      "non canonical JSON",
			index:     append([]byte("\n"), validIndex...),
			signature: sign(t, "2026-key-1", keyOne, append([]byte("\n"), validIndex...)),
		},
		{
			name:      "expired channel index",
			index:     canonicalExpiredIndex(t, hex.EncodeToString(manifestDigest[:])),
			signature: nil,
		},
		{
			name:      "non official manifest URL",
			index:     canonicalIndexWithURL(t, hex.EncodeToString(manifestDigest[:]), "https://example.com/manifest.json"),
			signature: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.signature == nil {
				tt.signature = sign(t, "2026-key-1", keyOne, tt.index)
			}
			if _, err := testVerifier().VerifyChannel(tt.index, tt.signature); err == nil {
				t.Fatal("VerifyChannel() error = nil")
			}
		})
	}
}

func TestVerifierRejectsManifestDigestAndUnsafeArtifact(t *testing.T) {
	validManifest := canonicalManifest(t)
	digest := sha256.Sum256(validManifest)
	indexBytes := canonicalIndex(t, hex.EncodeToString(digest[:]))
	index, err := testVerifier().VerifyChannel(indexBytes, sign(t, "2026-key-1", keyOne, indexBytes))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("digest mismatch", func(t *testing.T) {
		changed := append([]byte(nil), validManifest...)
		changed[len(changed)-2] ^= 1
		if _, err := testVerifier().VerifyManifest(index, changed, sign(t, "2026-key-2", keyTwo, changed)); err == nil {
			t.Fatal("VerifyManifest() error = nil")
		}
	})

	t.Run("path traversal artifact", func(t *testing.T) {
		unsafe := trust.Manifest{
			SchemaVersion:           1,
			ReleaseVersion:          "v1.2.1",
			Channel:                 "stable",
			PublishedAt:             "2026-08-30T00:00:00Z",
			ProtocolVersion:         1,
			CompatibleFromProtocols: []int{1},
			RollbackSafeFrom:        []string{"v1.2.0"},
			ShimProtocolMin:         1,
			ShimProtocolMax:         1,
			Artifacts: []trust.Artifact{{
				OS: "linux", Arch: "amd64", URL: "../procmesh.tar.gz", Size: 123, SHA256: string(make([]byte, 64)),
			}},
			ReleaseNotesURL: "https://github.com/xiaoyannzbbb/procmesh/releases/tag/v1.2.1",
		}
		unsafeBytes, err := trust.CanonicalJSON(unsafe)
		if err != nil {
			t.Fatal(err)
		}
		unsafeDigest := sha256.Sum256(unsafeBytes)
		unsafeIndexBytes := canonicalIndex(t, hex.EncodeToString(unsafeDigest[:]))
		unsafeIndex, err := testVerifier().VerifyChannel(unsafeIndexBytes, sign(t, "2026-key-1", keyOne, unsafeIndexBytes))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testVerifier().VerifyManifest(unsafeIndex, unsafeBytes, sign(t, "2026-key-2", keyTwo, unsafeBytes)); err == nil {
			t.Fatal("VerifyManifest() error = nil")
		}
	})
}

func testVerifier() trust.Verifier {
	return trust.Verifier{
		Keys: trust.Keyring{
			"2026-key-1": keyOne.Public().(ed25519.PublicKey),
			"2026-key-2": keyTwo.Public().(ed25519.PublicKey),
		},
		Now: func() time.Time { return time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC) },
	}
}

func canonicalIndex(t *testing.T, manifestDigest string) []byte {
	t.Helper()
	return canonicalIndexWithTimesAndURL(t, manifestDigest, "2026-08-30T00:00:00Z", "2026-08-31T00:00:00Z", "https://github.com/xiaoyannzbbb/procmesh/releases/download/v1.2.1/manifest.json")
}

func canonicalExpiredIndex(t *testing.T, manifestDigest string) []byte {
	t.Helper()
	return canonicalIndexWithTimesAndURL(t, manifestDigest, "2026-08-28T00:00:00Z", "2026-08-29T00:00:00Z", "https://github.com/xiaoyannzbbb/procmesh/releases/download/v1.2.1/manifest.json")
}

func canonicalIndexWithURL(t *testing.T, manifestDigest, manifestURL string) []byte {
	t.Helper()
	return canonicalIndexWithTimesAndURL(t, manifestDigest, "2026-08-30T00:00:00Z", "2026-08-31T00:00:00Z", manifestURL)
}

func canonicalIndexWithTimesAndURL(t *testing.T, manifestDigest, generatedAt, expiresAt, manifestURL string) []byte {
	t.Helper()
	b, err := trust.CanonicalJSON(trust.ChannelIndex{
		SchemaVersion: 1,
		Channel:       "stable",
		GeneratedAt:   generatedAt,
		ExpiresAt:     expiresAt,
		Release: trust.ChannelRelease{
			Version:              "v1.2.1",
			ManifestURL:          manifestURL,
			ManifestSHA256:       manifestDigest,
			ManifestSignatureURL: "https://github.com/xiaoyannzbbb/procmesh/releases/download/v1.2.1/manifest.json.sig",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func canonicalManifest(t *testing.T) []byte {
	t.Helper()
	b, err := trust.CanonicalJSON(trust.Manifest{
		SchemaVersion:           1,
		ReleaseVersion:          "v1.2.1",
		Channel:                 "stable",
		PublishedAt:             "2026-08-30T00:00:00Z",
		ProtocolVersion:         1,
		CompatibleFromProtocols: []int{1},
		RollbackSafeFrom:        []string{"v1.2.0"},
		ShimProtocolMin:         1,
		ShimProtocolMax:         1,
		Artifacts: []trust.Artifact{{
			OS: "linux", Arch: "amd64", URL: "procmesh_1.2.1_linux_amd64.tar.gz", Size: 123, SHA256: hex.EncodeToString(make([]byte, 32)),
		}},
		ReleaseNotesURL: "https://github.com/xiaoyannzbbb/procmesh/releases/tag/v1.2.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sign(t *testing.T, keyID string, privateKey ed25519.PrivateKey, payload []byte) []byte {
	t.Helper()
	b, err := trust.CanonicalJSON(trust.Signature{
		SchemaVersion: 1,
		KeyID:         keyID,
		Algorithm:     "ed25519",
		Signature:     base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
