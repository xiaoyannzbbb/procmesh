package releasemeta_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/update/releasemeta"
	"github.com/qleelulu/procmesh/internal/update/trust"
)

func TestGenerateProducesVerifiableReleaseMetadata(t *testing.T) {
	artifactDir := t.TempDir()
	artifactName := "procmesh_1.2.1_linux_amd64.tar.gz"
	if err := os.WriteFile(filepath.Join(artifactDir, artifactName), []byte("release archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	privateKey := testPrivateKey("release")
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	keyring := trust.Keyring{"2026-release": privateKey.Public().(ed25519.PublicKey)}

	files, err := releasemeta.Generate(releasemeta.Options{
		Version:          "v1.2.1",
		ArtifactDir:      artifactDir,
		RollbackSafeFrom: []string{"v1.2.0"},
		ProtocolVersion:  1,
		ShimProtocolMin:  1,
		ShimProtocolMax:  1,
		KeyID:            "2026-release",
		PrivateKey:       privateKey,
		TrustedKeys:      keyring,
		Now:              now,
		IndexValidity:    30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	verifier := trust.Verifier{Keys: keyring, Now: func() time.Time { return now.Add(time.Hour) }}
	index, err := verifier.VerifyChannel(files.ChannelIndex, files.ChannelSignature)
	if err != nil {
		t.Fatalf("VerifyChannel() error = %v", err)
	}
	manifest, err := verifier.VerifyManifest(index, files.Manifest, files.ManifestSignature)
	if err != nil {
		t.Fatalf("VerifyManifest() error = %v", err)
	}
	if got := manifest.Artifacts[0]; got.URL != artifactName || got.Size != int64(len("release archive")) {
		t.Fatalf("artifact = %#v", got)
	}
}

func TestGenerateRejectsSigningKeyOutsideEmbeddedRotationSet(t *testing.T) {
	artifactDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactDir, "procmesh_1.2.1_linux_amd64.tar.gz"), []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	privateKey := testPrivateKey("release")
	otherKey := testPrivateKey("other")

	_, err := releasemeta.Generate(releasemeta.Options{
		Version:          "v1.2.1",
		ArtifactDir:      artifactDir,
		RollbackSafeFrom: []string{"v1.2.0"},
		ProtocolVersion:  1,
		ShimProtocolMin:  1,
		ShimProtocolMax:  1,
		KeyID:            "2026-release",
		PrivateKey:       privateKey,
		TrustedKeys:      trust.Keyring{"2026-release": otherKey.Public().(ed25519.PublicKey)},
		Now:              time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
		IndexValidity:    24 * time.Hour,
	})
	if err == nil {
		t.Fatal("Generate() error = nil")
	}
}

func TestParseKeyRegistrySupportsOverlappingRotationKeys(t *testing.T) {
	first := testPrivateKey("first")
	second := testPrivateKey("second")
	registry, err := trust.CanonicalKeyRegistry(map[string]ed25519.PublicKey{
		"2026-release": first.Public().(ed25519.PublicKey),
		"2027-release": second.Public().(ed25519.PublicKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := trust.ParseKeyRegistry(registry)
	if err != nil {
		t.Fatalf("ParseKeyRegistry() error = %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("len(keys) = %d", len(keys))
	}
}

func testPrivateKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("procmesh releasemeta test " + label))
	return ed25519.NewKeyFromSeed(seed[:])
}
