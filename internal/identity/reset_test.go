package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/qleelulu/procmesh/internal/paths"
)

type resetMeta struct {
	id    string
	setTo []string
}

func (m *resetMeta) GetOrCreateNodeID(context.Context) (string, error) {
	return m.id, nil
}

func (m *resetMeta) SetNodeID(_ context.Context, id string) error {
	m.id = id
	m.setTo = append(m.setTo, id)
	return nil
}

func TestReset_RollsBackStoreWhenNodeIDFileWasNotCommitted(t *testing.T) {
	const oldID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	meta := &resetMeta{id: oldID}
	writeErr := errors.New("write failed before rename")

	_, err := reset(context.Background(), paths.New(t.TempDir()), meta, func(string, string) (bool, error) {
		return false, writeErr
	})

	if !errors.Is(err, writeErr) {
		t.Fatalf("error=%v want %v", err, writeErr)
	}
	if meta.id != oldID {
		t.Fatalf("store node_id=%q want rollback to %q", meta.id, oldID)
	}
	if len(meta.setTo) != 2 || meta.setTo[0] == oldID || meta.setTo[1] != oldID {
		t.Fatalf("SetNodeID calls=%q want [new, old]", meta.setTo)
	}
}

func TestReset_DoesNotRollBackStoreWhenNodeIDFileWasCommitted(t *testing.T) {
	const oldID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	meta := &resetMeta{id: oldID}
	syncErr := errors.New("directory sync failed after rename")

	_, err := reset(context.Background(), paths.New(t.TempDir()), meta, func(string, string) (bool, error) {
		return true, syncErr
	})

	if !errors.Is(err, syncErr) {
		t.Fatalf("error=%v want %v", err, syncErr)
	}
	if meta.id == oldID {
		t.Fatalf("store node_id was rolled back after file commit")
	}
	if len(meta.setTo) != 1 || meta.setTo[0] != meta.id {
		t.Fatalf("SetNodeID calls=%q want only committed id %q", meta.setTo, meta.id)
	}
}
