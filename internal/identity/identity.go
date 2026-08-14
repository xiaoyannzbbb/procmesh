package identity

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/paths"
)

const idFileMode = 0o640

var nodeIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Meta is the store surface needed to keep node_id in sync with the file.
type Meta interface {
	GetOrCreateNodeID(ctx context.Context) (string, error)
	SetNodeID(ctx context.Context, id string) error
}

// Ensure aligns $data_dir/node_id and $data_dir/boot_id with the store.
// File node_id wins when present and valid; otherwise store GetOrCreateNodeID
// is used and written to the file. boot_id file is always overwritten with hostBoot.
// Does not rotate or rewrite store boot_id.
func Ensure(ctx context.Context, layout paths.Layout, meta Meta, hostBoot string) (nodeID string, err error) {
	if err := layout.Ensure(); err != nil {
		return "", fmt.Errorf("ensure layout: %w", err)
	}

	raw, err := os.ReadFile(layout.NodeIDFile())
	switch {
	case err == nil:
		id := strings.TrimSpace(string(raw))
		if !nodeIDRE.MatchString(id) {
			return "", errcode.E(errcode.INVALID, "node_id must be a UUID")
		}
		if err := meta.SetNodeID(ctx, id); err != nil {
			return "", err
		}
		nodeID = id
	case os.IsNotExist(err):
		id, err := meta.GetOrCreateNodeID(ctx)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(layout.NodeIDFile(), []byte(id+"\n"), idFileMode); err != nil {
			return "", fmt.Errorf("write node_id: %w", err)
		}
		nodeID = id
	default:
		return "", fmt.Errorf("read node_id: %w", err)
	}

	if err := os.WriteFile(layout.BootIDFile(), []byte(hostBoot+"\n"), idFileMode); err != nil {
		return "", fmt.Errorf("write boot_id: %w", err)
	}
	return nodeID, nil
}
