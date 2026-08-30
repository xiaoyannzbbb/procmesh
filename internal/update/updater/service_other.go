//go:build !linux

package updater

import (
	"context"
	"errors"
)

type SystemdService struct{}

func NewSystemdService() (*SystemdService, error) {
	return nil, errors.New("managed ProcMesh updates require Linux systemd")
}

func (*SystemdService) RestartAgent(context.Context) error {
	return errors.New("managed ProcMesh updates require Linux systemd")
}

func (*SystemdService) RunningAgentPath(context.Context) (string, error) {
	return "", errors.New("managed ProcMesh updates require Linux systemd")
}
