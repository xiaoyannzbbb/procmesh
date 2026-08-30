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
		os.Exit(86)
	}
	return nil
}
