//go:build !updateaccept

package main

import (
	"github.com/qleelulu/procmesh/internal/update/trust"
	"github.com/qleelulu/procmesh/internal/update/updater"
)

func updaterKeyring() (trust.Keyring, error) { return trust.DefaultKeyring() }

func updaterCheckpoint(updater.Phase) error { return nil }
