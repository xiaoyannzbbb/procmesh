package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// Encode marshals s to JSON and returns the payload plus lowercase hex SHA-256
// of the full encoded bytes.
func Encode(s Snapshot) (payload []byte, sha256hex string, err error) {
	payload, err = json.Marshal(s)
	if err != nil {
		return nil, "", fmt.Errorf("encode snapshot: %w", err)
	}
	sum := sha256.Sum256(payload)
	return payload, hex.EncodeToString(sum[:]), nil
}

// Decode unmarshals a format_version=1 snapshot payload.
// format_version != 1 or missing snapshot_id returns INVALID.
func Decode(payload []byte) (Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(payload, &s); err != nil {
		return Snapshot{}, errcode.E(errcode.INVALID, "snapshot json: "+err.Error())
	}
	if s.FormatVersion != 1 {
		return Snapshot{}, errcode.E(errcode.INVALID, "unsupported format_version")
	}
	if s.SnapshotID == "" {
		return Snapshot{}, errcode.E(errcode.INVALID, "snapshot_id required")
	}
	return s, nil
}

// LatestSpec returns the Spec for MaxRevision as raw JSON (no re-encoding).
func LatestSpec(p ProcessDump) (json.RawMessage, error) {
	for _, r := range p.Revisions {
		if r.Revision == p.MaxRevision {
			return r.Spec, nil
		}
	}
	return nil, errcode.E(errcode.INVALID, "max revision spec missing")
}
