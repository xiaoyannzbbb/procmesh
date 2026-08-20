package backup_test

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/backup"
)

func TestPolicyFromRecordCopiesControlShapedPolicy(t *testing.T) {
	record := backup.PolicyRecord{
		PolicyID: "bp-1", Name: "nightly", Enabled: true,
		ScheduleCron: "0 2 * * *", Timezone: "Asia/Shanghai",
		TargetSelector: "EXPLICIT_NODES", TargetIDs: []string{"node-a"},
		Sink: "s3", DestinationProfile: "archive", RetentionKeepLast: 7,
		TimeoutSeconds: 30, MaxConcurrency: 2, UnavailablePolicy: "FAIL_FAST", Revision: 4,
	}
	got := backup.PolicyFromRecord(record)
	if got.PolicyID != "bp-1" || got.DestinationProfile != "archive" || got.Revision != 4 {
		t.Fatalf("policy=%+v", got)
	}
	record.TargetIDs[0] = "changed"
	if got.TargetIDs[0] != "node-a" {
		t.Fatalf("target ids alias input: %v", got.TargetIDs)
	}
}
