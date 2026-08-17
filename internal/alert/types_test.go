package alert_test

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/alert"
)

func TestFingerprint_AgentFailedUsesNodeFailedPrefix(t *testing.T) {
	if alert.Fingerprint(alert.TypeAgentFailed, "n1", "", "") != "NODE_FAILED:n1" {
		t.Fatal(alert.Fingerprint(alert.TypeAgentFailed, "n1", "", ""))
	}
	if alert.Fingerprint(alert.TypeAgentSuspect, "n1", "", "") != "NODE_SUSPECT:n1" {
		t.Fatal("suspect")
	}
	if alert.Fingerprint(alert.TypeControlNoQuorum, "", "", "cid") != "CONTROL_NO_QUORUM:cid" {
		t.Fatal("quorum")
	}
	if alert.Fingerprint(alert.TypeCPUHigh, "n1", "p1", "") != "CPU_HIGH:p1" {
		t.Fatal("cpu proc")
	}
	if alert.Fingerprint(alert.TypeCPUHigh, "n1", "", "") != "CPU_HIGH:n1" {
		t.Fatal("cpu node")
	}
}
