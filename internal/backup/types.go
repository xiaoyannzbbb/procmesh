package backup

import (
	"encoding/json"
	"time"
)

// Snapshot is the format_version=1 backup payload.
type Snapshot struct {
	FormatVersion int           `json:"format_version"`
	SnapshotID    string        `json:"snapshot_id"`
	ClusterID     string        `json:"cluster_id"`
	NodeID        string        `json:"node_id"`
	PolicyID      string        `json:"policy_id,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	Processes     []ProcessDump `json:"processes"`
}

// ProcessDump is one process and its revision history in a snapshot.
type ProcessDump struct {
	ProcessID   string         `json:"process_id"`
	Name        string         `json:"name"`
	MinRevision int64          `json:"min_revision"`
	MaxRevision int64          `json:"max_revision"`
	Revisions   []RevisionDump `json:"revisions"`
}

// RevisionDump is one config revision payload taken from store.spec_json.
type RevisionDump struct {
	Revision  int64           `json:"revision"`
	Operator  string          `json:"operator"`
	Timestamp time.Time       `json:"timestamp"`
	Diff      string          `json:"diff"`
	Comment   string          `json:"comment"`
	Spec      json.RawMessage `json:"spec"`
}

// Meta is local index metadata for a stored snapshot (not the full payload).
type Meta struct {
	SnapshotID     string
	ClusterID      string
	NodeID         string
	CreatedAt      time.Time
	ProcessIDs     []string
	RevisionRanges []RevisionRange
	SHA256         string
	Bytes          int64
	Sink           string // fs | s3 | peer
	Location       string
	SourceNodeID   string // peer receive source; empty when self-created
}

// RevisionRange summarizes min/max revision for one process in a snapshot.
type RevisionRange struct {
	ProcessID   string `json:"process_id"`
	MinRevision int64  `json:"min_revision"`
	MaxRevision int64  `json:"max_revision"`
}

// MetaFromSnapshot builds Meta identity and process ranges from a Snapshot.
// Sink, Location, SHA256, and SourceNodeID are left for the caller to fill.
func MetaFromSnapshot(s Snapshot) Meta {
	m := Meta{
		SnapshotID: s.SnapshotID,
		ClusterID:  s.ClusterID,
		NodeID:     s.NodeID,
		CreatedAt:  s.CreatedAt,
	}
	if len(s.Processes) == 0 {
		m.ProcessIDs = []string{}
		m.RevisionRanges = []RevisionRange{}
		return m
	}
	m.ProcessIDs = make([]string, 0, len(s.Processes))
	m.RevisionRanges = make([]RevisionRange, 0, len(s.Processes))
	for _, p := range s.Processes {
		m.ProcessIDs = append(m.ProcessIDs, p.ProcessID)
		m.RevisionRanges = append(m.RevisionRanges, RevisionRange{
			ProcessID:   p.ProcessID,
			MinRevision: p.MinRevision,
			MaxRevision: p.MaxRevision,
		})
	}
	return m
}
