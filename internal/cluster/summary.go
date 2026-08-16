package cluster

type State string

const (
	StateJoining State = "JOINING"
	StateAlive   State = "ALIVE"
	StateSuspect State = "SUSPECT"
	StateFailed  State = "FAILED"
	StateLeft    State = "LEFT"
	StateRemoved State = "REMOVED"
	StateRevoked State = "REVOKED"
)

type ProcessSummary struct {
	ProcessID       string `json:"process_id,omitempty"`
	Name            string `json:"name"`
	Group           string `json:"group,omitempty"`
	Desired         string `json:"desired"`
	Observed        string `json:"observed"`
	Health          string `json:"health"`
	LatestRevision  int64  `json:"latest_revision"`
	ActiveRevision  int64  `json:"active_revision"`
	FreshnessUnixMs int64  `json:"freshness_unix_ms"`
}

type ResourceSummary struct {
	CPUPercent    int `json:"cpu_percent"`    // -1 = unknown / not collected
	MemoryPercent int `json:"memory_percent"` // -1 = unknown / not collected
	DiskPercent   int `json:"disk_percent"`   // -1 = unknown / not collected
}

type NodeSummary struct {
	NodeID            string            `json:"node_id"`
	ClusterID         string            `json:"cluster_id"`
	Hostname          string            `json:"hostname"`
	BootID            string            `json:"boot_id"`
	State             State             `json:"state"`
	AgentVersion      string            `json:"agent_version"`
	ProtocolVersion   int               `json:"protocol_version"`
	APIAddress        string            `json:"api_address"`
	RPCAddress        string            `json:"rpc_address"`
	GossipAddress     string            `json:"gossip_address"`
	Labels            map[string]string `json:"labels,omitempty"`
	Resources         ResourceSummary   `json:"resources"`
	Processes         []ProcessSummary  `json:"processes,omitempty"`
	LastUpdatedUnixMs int64             `json:"last_updated_unix_ms"`
}

type SummarySource interface {
	Snapshot() NodeSummary
}
