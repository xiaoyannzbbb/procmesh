package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/store"
	"golang.org/x/sys/unix"
)

func cleanupDataDir(root string) {
	if root == "" {
		return
	}
	layout := paths.New(root)
	entries, err := os.ReadDir(layout.RuntimeDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(layout.RuntimeDir, e.Name()))
			if err != nil {
				continue
			}
			var snap struct {
				PID     int `json:"pid"`
				ShimPID int `json:"shim_pid"`
			}
			if json.Unmarshal(data, &snap) != nil {
				continue
			}
			killAgentPIDs(snap.PID, snap.ShimPID)
		}
	}
	st, err := store.Open(layout.Store)
	if err != nil {
		return
	}
	defer func() { _ = st.Close() }()
	specs, err := st.ListSpecs(context.Background())
	if err != nil {
		return
	}
	for _, spec := range specs {
		insts, err := st.ListInstances(context.Background(), spec.ProcessID)
		if err != nil {
			continue
		}
		for _, inst := range insts {
			killAgentPIDs(inst.PID, inst.ShimPID)
		}
	}
}

func killAgentPIDs(pid, shimPID int) {
	self := os.Getpid()
	killOne := func(p int) {
		if p <= 1 || p == self {
			return
		}
		_ = unix.Kill(p, unix.SIGKILL)
	}
	killOne(pid)
	killOne(shimPID)
}
