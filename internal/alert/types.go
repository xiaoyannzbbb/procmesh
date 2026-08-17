package alert

type Type string
type Severity string
type State string

const (
	TypeProcessExit      Type = "PROCESS_EXIT"
	TypeProcessFatal     Type = "PROCESS_FATAL"
	TypeProcessCrashLoop Type = "PROCESS_CRASH_LOOP"
	TypeHealthFailed     Type = "HEALTH_FAILED"
	TypeCPUHigh          Type = "CPU_HIGH"
	TypeMemoryHigh       Type = "MEMORY_HIGH"
	TypeDiskHigh         Type = "DISK_HIGH"
	TypeLocalDBError     Type = "LOCAL_DB_ERROR"
	TypeAgentFailed      Type = "AGENT_FAILED"
	TypeAgentSuspect     Type = "AGENT_SUSPECT_TOO_LONG"
	TypeControlNoQuorum  Type = "CONTROL_NO_QUORUM"
	TypeCertExpiring     Type = "CERT_EXPIRING"
	TypeVersionMismatch  Type = "VERSION_MISMATCH"

	SevWarning  Severity = "WARNING"
	SevCritical Severity = "CRITICAL"

	StateFiring   State = "FIRING"
	StateResolved State = "RESOLVED"
)

func Fingerprint(typ Type, nodeID, processID, clusterID string) string {
	switch typ {
	case TypeProcessExit, TypeProcessFatal, TypeProcessCrashLoop, TypeHealthFailed:
		return string(typ) + ":" + processID
	case TypeCPUHigh, TypeMemoryHigh:
		id := processID
		if id == "" {
			id = nodeID
		}
		return string(typ) + ":" + id
	case TypeDiskHigh, TypeLocalDBError, TypeCertExpiring, TypeVersionMismatch:
		return string(typ) + ":" + nodeID
	case TypeAgentFailed:
		return "NODE_FAILED:" + nodeID
	case TypeAgentSuspect:
		return "NODE_SUSPECT:" + nodeID
	case TypeControlNoQuorum:
		return "CONTROL_NO_QUORUM:" + clusterID
	default:
		return string(typ) + ":" + nodeID
	}
}

func DefaultSeverity(typ Type) Severity {
	switch typ {
	case TypeProcessFatal, TypeProcessCrashLoop, TypeLocalDBError, TypeAgentFailed, TypeControlNoQuorum:
		return SevCritical
	default:
		return SevWarning
	}
}

func requiresProcessID(typ Type) bool {
	switch typ {
	case TypeProcessExit, TypeProcessFatal, TypeProcessCrashLoop, TypeHealthFailed:
		return true
	default:
		return false
	}
}
