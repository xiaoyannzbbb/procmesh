package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestConfig_HistoryAfterTwoApplies(t *testing.T) {
	ctx := context.Background()
	proc, cfg := newConfigClients(t)
	seedWebV1V2(t, proc)

	got, err := cfg.History(ctx, connect.NewRequest(&procmeshv1.HistoryRequest{IdOrName: "web"}))
	if err != nil {
		t.Fatal(err)
	}
	revs := got.Msg.GetRevisions()
	if len(revs) != 2 {
		t.Fatalf("len=%d", len(revs))
	}
	if revs[0].GetRevision() != 1 || revs[1].GetRevision() != 2 {
		t.Fatalf("revs %+v", revs)
	}
	if revs[1].GetOperator() != "admin" || revs[1].GetComment() != "tune" || revs[1].GetDiff() == "" {
		t.Fatalf("meta %+v", revs[1])
	}
	if !strings.Contains(revs[1].GetDiff(), "args") && !strings.Contains(revs[1].GetDiff(), "command") {
		t.Fatalf("diff %q", revs[1].GetDiff())
	}
}

func TestConfig_DiffContainsChangedFields(t *testing.T) {
	ctx := context.Background()
	proc, cfg := newConfigClients(t)
	seedWebV1V2(t, proc)

	got, err := cfg.Diff(ctx, connect.NewRequest(&procmeshv1.DiffRequest{
		IdOrName:     "web",
		FromRevision: 1,
		ToRevision:   2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	diff := got.Msg.GetDiff()
	if diff == "" {
		t.Fatal("empty diff")
	}
	if !strings.Contains(diff, "-command") || !strings.Contains(diff, "+command") {
		t.Fatalf("diff %q", diff)
	}
	if !strings.Contains(diff, "-args v1") || !strings.Contains(diff, "+args v2") {
		t.Fatalf("diff %q", diff)
	}
}

func TestConfig_RollbackRestoresV1(t *testing.T) {
	ctx := context.Background()
	proc, cfg := newConfigClients(t)
	seedWebV1V2(t, proc)

	got, err := cfg.Rollback(ctx, connect.NewRequest(&procmeshv1.RollbackRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-rb", Operator: "admin"},
		IdOrName:         "web",
		ToRevision:       1,
		ExpectedRevision: 2,
		Comment:          "undo",
	}))
	if err != nil {
		t.Fatal(err)
	}
	spec := got.Msg.GetSpec()
	if spec.GetLatestRevision() != 3 {
		t.Fatalf("latest=%d", spec.GetLatestRevision())
	}
	if spec.GetCommand() != "/bin/echo" || len(spec.GetArgs()) != 1 || spec.GetArgs()[0] != "v1" {
		t.Fatalf("spec %+v", spec)
	}
}

func TestConfig_UpdateConflictRevision(t *testing.T) {
	ctx := context.Background()
	proc, cfg := newConfigClients(t)
	if _, err := proc.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-c", Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	})); err != nil {
		t.Fatal(err)
	}
	_, err := cfg.UpdateConfig(ctx, connect.NewRequest(&procmeshv1.UpdateConfigRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-u", Operator: "t"},
		IdOrName:         "web",
		ExpectedRevision: 99,
		Spec:             &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeFailedPrecondition || detail != "CONFLICT" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestConfig_UpdateRequiresExpectedRevision(t *testing.T) {
	ctx := context.Background()
	_, cfg := newConfigClients(t)
	_, err := cfg.UpdateConfig(ctx, connect.NewRequest(&procmeshv1.UpdateConfigRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-u", Operator: "t"},
		IdOrName:         "web",
		ExpectedRevision: 0,
		Spec:             &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestConfig_DiffMissingRevision(t *testing.T) {
	ctx := context.Background()
	proc, cfg := newConfigClients(t)
	seedWebV1V2(t, proc)
	_, err := cfg.Diff(ctx, connect.NewRequest(&procmeshv1.DiffRequest{
		IdOrName:     "web",
		FromRevision: 1,
		ToRevision:   9,
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestConfig_MissingOperationID(t *testing.T) {
	ctx := context.Background()
	_, cfg := newConfigClients(t)

	_, err := cfg.UpdateConfig(ctx, connect.NewRequest(&procmeshv1.UpdateConfigRequest{
		Meta:             &procmeshv1.MutationMeta{Operator: "t"},
		IdOrName:         "web",
		ExpectedRevision: 1,
		Spec:             &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/true"},
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("update code=%v detail=%s err=%v", code, detail, err)
	}

	_, err = cfg.Rollback(ctx, connect.NewRequest(&procmeshv1.RollbackRequest{
		Meta:             &procmeshv1.MutationMeta{Operator: "t"},
		IdOrName:         "web",
		ToRevision:       1,
		ExpectedRevision: 1,
	}))
	code, detail = connectDetail(t, err)
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("rollback code=%v detail=%s err=%v", code, detail, err)
	}
}

func seedWebV1V2(t *testing.T, proc procmeshv1connect.ProcessServiceClient) {
	t.Helper()
	ctx := context.Background()
	first, err := proc.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta:    &procmeshv1.MutationMeta{OperationId: "op-v1", Operator: "admin"},
		Spec:    &procmeshv1.ProcessSpec{Name: "web", Command: "/bin/echo", Args: []string{"v1"}},
		Comment: "create",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta:             &procmeshv1.MutationMeta{OperationId: "op-v2", Operator: "admin"},
		ExpectedRevision: 1,
		Spec: &procmeshv1.ProcessSpec{
			ProcessId: first.Msg.GetSpec().GetProcessId(),
			Name:      "web",
			Command:   "/bin/sleep",
			Args:      []string{"v2"},
		},
		Comment: "tune",
	})); err != nil {
		t.Fatal(err)
	}
}
