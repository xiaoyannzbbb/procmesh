package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/update/releasemeta"
	"github.com/qleelulu/procmesh/internal/update/trust"
	"github.com/qleelulu/procmesh/internal/version"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("procmesh-release-metadata", flag.ContinueOnError)
	flags.SetOutput(stderr)
	releaseVersion := flags.String("version", "", "release version in vMAJOR.MINOR.PATCH form")
	artifactDir := flags.String("artifact-dir", "", "directory containing release archives")
	rollbackSafeFrom := flags.String("rollback-safe-from", "", "comma-separated versions safe to roll back to")
	keyID := flags.String("key-id", "", "trusted release signing key id")
	privateKeyPath := flags.String("private-key", "", "root-readable file containing a base64 Ed25519 seed or private key")
	trustedKeysPath := flags.String("trusted-keys", "internal/update/trust/trusted_keys.json", "trusted public key registry")
	indexValidity := flags.Duration("index-validity", 30*24*time.Hour, "signed channel index validity")
	publishedAt := flags.String("published-at", "", "UTC RFC3339 publish time (defaults to now)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *releaseVersion == "" || *artifactDir == "" || *rollbackSafeFrom == "" || *keyID == "" || *privateKeyPath == "" {
		fmt.Fprintln(stderr, "version, artifact-dir, rollback-safe-from, key-id, and private-key are required")
		return 2
	}
	now := time.Now().UTC().Truncate(time.Second)
	if *publishedAt != "" {
		parsed, err := time.Parse(time.RFC3339, *publishedAt)
		if err != nil || parsed.Format(time.RFC3339) != *publishedAt || !strings.HasSuffix(*publishedAt, "Z") {
			fmt.Fprintln(stderr, "published-at must be canonical UTC RFC3339")
			return 2
		}
		now = parsed
	}
	privateKey, err := loadPrivateKey(*privateKeyPath)
	if err != nil {
		fmt.Fprintln(stderr, "unable to load release signing key")
		return 1
	}
	registryBytes, err := os.ReadFile(*trustedKeysPath)
	if err != nil {
		fmt.Fprintln(stderr, "unable to read trusted public key registry")
		return 1
	}
	trustedKeys, err := trust.ParseKeyRegistry(registryBytes)
	if err != nil {
		fmt.Fprintln(stderr, "trusted public key registry is invalid")
		return 1
	}
	files, err := releasemeta.Generate(releasemeta.Options{
		Version:          *releaseVersion,
		ArtifactDir:      *artifactDir,
		RollbackSafeFrom: splitVersions(*rollbackSafeFrom),
		ProtocolVersion:  version.Protocol,
		ShimProtocolMin:  version.Protocol,
		ShimProtocolMax:  version.Protocol,
		KeyID:            *keyID,
		PrivateKey:       privateKey,
		TrustedKeys:      trustedKeys,
		Now:              now,
		IndexValidity:    *indexValidity,
	})
	if err != nil {
		fmt.Fprintf(stderr, "generate signed release metadata: %v\n", err)
		return 1
	}
	outputs := map[string][]byte{
		"manifest.json":     files.Manifest,
		"manifest.json.sig": files.ManifestSignature,
		"stable.json":       files.ChannelIndex,
		"stable.json.sig":   files.ChannelSignature,
	}
	for name, payload := range outputs {
		if err := writeAtomic(filepath.Join(*artifactDir, name), payload, 0o644); err != nil {
			fmt.Fprintln(stderr, "write signed release metadata failed")
			return 1
		}
	}
	fmt.Fprintf(stdout, "Created signed release metadata in %s\n", *artifactDir)
	return 0
}

func loadPrivateKey(name string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 1024 {
		return nil, errors.New("private key file must be private and regular")
	}
	payload, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if block, _ := pem.Decode(payload); block != nil {
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		privateKey, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not Ed25519")
		}
		return privateKey, nil
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(payload)))
	if err != nil {
		return nil, err
	}
	switch len(decoded) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(decoded), nil
	case ed25519.PrivateKeySize:
		privateKey := ed25519.PrivateKey(decoded)
		if !bytes.Equal(privateKey[ed25519.SeedSize:], privateKey.Public().(ed25519.PublicKey)) {
			return nil, errors.New("invalid Ed25519 private key")
		}
		return privateKey, nil
	default:
		return nil, errors.New("invalid Ed25519 key length")
	}
}

func splitVersions(value string) []string {
	parts := strings.Split(value, ",")
	versions := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			versions = append(versions, trimmed)
		}
	}
	return versions
}

func writeAtomic(name string, payload []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(name), ".release-metadata-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, name)
}
