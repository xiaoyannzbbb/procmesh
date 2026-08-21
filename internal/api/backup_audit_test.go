package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestBackupAuditRedactsSecrets(t *testing.T) {
	_, st, _ := newTestManager(t)
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})
	auditControlMutation(ctx, st, "node-a", controlMutation{
		Action:      "backup.policy.create",
		Resource:    "backup_policy:bp-secret",
		OperationID: "op-redact",
		PolicyID:    "bp-secret",
		Result:      "FAILED",
		Error:       "upload failed secret_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY access_key=AKIAIOSFODNN7EXAMPLE payload=\x00\x01",
	})
	assertControlAudit(t, st, "backup.policy.create", "FAILED", map[string]string{"policy_id": "bp-secret"})
}

func TestBackupRetentionAudit(t *testing.T) {
	_, st, _ := newTestManager(t)
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-admin", Username: "admin"})
	auditControlMutation(ctx, st, "node-a", controlMutation{
		Action:      "backup.retention.delete",
		Resource:    "backup_policy:bp-1",
		OperationID: "op-retention",
		PolicyID:    "bp-1",
		RunID:       "run-1",
		TaskID:      "task-1",
		SnapshotID:  "snap-1",
		Result:      "SUCCESS",
		Error:       "s3://AKIAIOSFODNN7EXAMPLE:wJalrXUtnFEMI@bucket/key",
	})
	assertControlAudit(t, st, "backup.retention.delete", "SUCCESS", map[string]string{
		"policy_id": "bp-1", "run_id": "run-1", "task_id": "task-1", "snapshot_id": "snap-1",
	})
}

func assertControlAudit(t *testing.T, st *store.Store, action, result string, ids map[string]string) {
	t.Helper()
	if st == nil {
		t.Fatal("store required")
	}
	events, err := st.ListAuditAll(context.Background(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var found *store.AuditEvent
	for i := range events {
		if events[i].Action == action && events[i].Result == result {
			ev := events[i]
			found = &ev
			break
		}
	}
	if found == nil {
		t.Fatalf("missing audit action=%s result=%s in %s", action, result, auditBodies(events))
	}
	if found.UserID != "user-admin" || found.Username != "admin" {
		t.Fatalf("audit user %+v", found)
	}
	if found.Timestamp.IsZero() {
		t.Fatal("audit timestamp required")
	}
	meta := map[string]string{}
	if len(found.Metadata) > 0 {
		if err := json.Unmarshal(found.Metadata, &meta); err != nil {
			t.Fatalf("metadata %s: %v", found.Metadata, err)
		}
	}
	for key, want := range ids {
		if meta[key] != want {
			t.Fatalf("metadata %s=%q want %q in %s", key, meta[key], want, found.Metadata)
		}
	}
	raw := string(found.Metadata) + found.Resource + found.Action + found.OperationID
	for _, leaked := range []string{
		"secret_key", "access_key", "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI",
		"payload=", "\x00\x01", "s3://", "/var/lib/procmesh",
	} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("audit leaked %q: %s", leaked, raw)
		}
	}
}

func auditBodies(events []store.AuditEvent) string {
	var b strings.Builder
	for _, ev := range events {
		b.WriteString(ev.Action)
		b.WriteString("=")
		b.WriteString(ev.Result)
		b.WriteString(" ")
		b.Write(ev.Metadata)
		b.WriteString("\n")
	}
	return b.String()
}
