package backup_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestEncodeDecode_RoundTripAndSHA256(t *testing.T) {
	s := backup.Snapshot{
		FormatVersion: 1,
		SnapshotID:    "snap-1",
		ClusterID:     "c1",
		NodeID:        "n1",
		CreatedAt:     time.Unix(1_700_000_000, 0).UTC(),
		Processes: []backup.ProcessDump{{
			ProcessID: "p1", Name: "web", MinRevision: 1, MaxRevision: 2,
			Revisions: []backup.RevisionDump{
				{Revision: 1, Operator: "a", Timestamp: time.Unix(1_700_000_000, 0).UTC(), Spec: json.RawMessage(`{"Name":"web"}`)},
				{Revision: 2, Operator: "b", Timestamp: time.Unix(1_700_000_100, 0).UTC(), Spec: json.RawMessage(`{"Name":"web","Command":"/bin/web"}`)},
			},
		}},
	}
	payload, sum, err := backup.Encode(s)
	if err != nil || sum == "" || !json.Valid(payload) {
		t.Fatalf("encode %q %v", sum, err)
	}
	got, err := backup.Decode(payload)
	if err != nil || got.SnapshotID != "snap-1" || got.Processes[0].MaxRevision != 2 {
		t.Fatalf("decode %+v %v", got, err)
	}
	if _, sum2, _ := backup.Encode(got); sum2 != sum {
		t.Fatalf("sha mismatch %s %s", sum, sum2)
	}
}

func TestDecode_RejectsBadVersion(t *testing.T) {
	_, err := backup.Decode([]byte(`{"format_version":2,"snapshot_id":"x"}`))
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err %v", err)
	}
}

func TestLatestSpec_ReturnsMaxRevisionRawJSON(t *testing.T) {
	raw, err := backup.LatestSpec(backup.ProcessDump{
		MaxRevision: 2,
		Revisions: []backup.RevisionDump{
			{Revision: 1, Spec: json.RawMessage(`{"A":1}`)},
			{Revision: 2, Spec: json.RawMessage(`{"A":2}`)},
		},
	})
	if err != nil || string(raw) != `{"A":2}` {
		t.Fatalf("%s %v", raw, err)
	}
}
