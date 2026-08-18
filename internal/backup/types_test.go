package backup_test

import (
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestMetaFromSnapshot_ProcessIDsAndRanges(t *testing.T) {
	s := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "c1",
		NodeID:        "n1",
		CreatedAt:     time.Unix(1_700_000_000, 0).UTC(),
		Processes: []backup.ProcessDump{
			{ProcessID: "p1", Name: "web", MinRevision: 1, MaxRevision: 2},
			{ProcessID: "p2", Name: "api", MinRevision: 3, MaxRevision: 5},
		},
	}
	m := backup.MetaFromSnapshot(s)
	if m.SnapshotID != "snap-1" || m.ClusterID != "c1" || m.NodeID != "n1" {
		t.Fatalf("%+v", m)
	}
	if len(m.ProcessIDs) != 2 || m.ProcessIDs[0] != "p1" || m.ProcessIDs[1] != "p2" {
		t.Fatalf("ProcessIDs %+v", m.ProcessIDs)
	}
	if len(m.RevisionRanges) != 2 ||
		m.RevisionRanges[0].ProcessID != "p1" || m.RevisionRanges[0].MinRevision != 1 || m.RevisionRanges[0].MaxRevision != 2 ||
		m.RevisionRanges[1].ProcessID != "p2" || m.RevisionRanges[1].MinRevision != 3 || m.RevisionRanges[1].MaxRevision != 5 {
		t.Fatalf("RevisionRanges %+v", m.RevisionRanges)
	}
	if !m.CreatedAt.Equal(s.CreatedAt) {
		t.Fatalf("CreatedAt %v", m.CreatedAt)
	}
}

func TestDecode_RejectsMissingSnapshotID(t *testing.T) {
	_, err := backup.Decode([]byte(`{"format_version":1}`))
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err %v", err)
	}
}
