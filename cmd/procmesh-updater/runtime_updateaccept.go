//go:build updateaccept

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"strings"

	"github.com/qleelulu/procmesh/internal/update/trust"
	"github.com/qleelulu/procmesh/internal/update/updater"
)

const acceptancePublicKey = "xyMahjmLYOPoCzVMu93x9cACvqkgEzBD7TIf5ErO+MA="
const acceptanceFailpoint = "/run/procmesh-update-accept-failpoint"
const acceptanceHealthProbeMarker = "/var/lib/procmesh/update/u0-health-probe-issued"

func updaterKeyring() (trust.Keyring, error) {
	publicKey, err := base64.StdEncoding.Strict().DecodeString(acceptancePublicKey)
	if err != nil {
		return nil, err
	}
	return trust.Keyring{"u0-acceptance": ed25519.PublicKey(publicKey)}, nil
}

func updaterCheckpoint(phase updater.Phase) error {
	payload, err := os.ReadFile(acceptanceFailpoint)
	if err == nil && strings.TrimSpace(string(payload)) == string(phase) {
		if phase == updater.PhaseHealthChecking {
			if err := writeAcceptanceMarker(acceptanceHealthProbeMarker, "first health probe issued\n"); err != nil {
				return err
			}
		}
		os.Exit(86)
	}
	return nil
}

func writeAcceptanceMarker(name, payload string) error {
	file, err := os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	directory, err := os.Open(managedUpdateRoot)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
