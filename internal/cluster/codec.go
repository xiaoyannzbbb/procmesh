package cluster

import "encoding/json"

// EncodeMeta serializes identity fields for memberlist NodeMeta.
// Processes are omitted so the payload stays under the 512-byte NodeMeta limit.
func EncodeMeta(s NodeSummary) []byte {
	s.Processes = nil
	raw, err := json.Marshal(s)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

func DecodeMeta(b []byte) (NodeSummary, error) {
	var s NodeSummary
	if err := json.Unmarshal(b, &s); err != nil {
		return NodeSummary{}, err
	}
	s.Processes = nil
	return s, nil
}

// EncodeState serializes the full node summary for LocalState / MergeRemoteState.
func EncodeState(s NodeSummary) []byte {
	raw, err := json.Marshal(s)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

func DecodeState(b []byte) (NodeSummary, error) {
	var s NodeSummary
	if err := json.Unmarshal(b, &s); err != nil {
		return NodeSummary{}, err
	}
	return s, nil
}
