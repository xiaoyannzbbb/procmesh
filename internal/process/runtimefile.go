package process

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qleelulu/procmesh/internal/paths"
	"golang.org/x/sys/unix"
)

type runtimeSnapshot struct {
	PID     int    `json:"pid"`
	ShimPID int    `json:"shim_pid"`
	BootID  string `json:"boot_id"`
}

func runtimePath(layout paths.Layout, instanceID string) string {
	return filepath.Join(layout.RuntimeDir, sanitizedID(instanceID)+".json")
}

func sanitizedID(id string) string {
	return strings.ReplaceAll(id, ":", "_")
}

func writeRuntime(layout paths.Layout, inst Instance) error {
	path := runtimePath(layout, inst.InstanceID)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir runtime: %w", err)
	}
	snap := runtimeSnapshot{PID: inst.PID, ShimPID: inst.ShimPID, BootID: inst.BootID}
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal runtime: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	return os.Rename(tmp, path)
}

func readRuntime(layout paths.Layout, instanceID string) (runtimeSnapshot, error) {
	data, err := os.ReadFile(runtimePath(layout, instanceID))
	if err != nil {
		return runtimeSnapshot{}, err
	}
	var snap runtimeSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return runtimeSnapshot{}, err
	}
	return snap, nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || err == unix.EPERM
}
