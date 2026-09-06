package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

type membershipNodeStub struct {
	procmeshv1connect.UnimplementedNodeServiceHandler
	report        *procmeshv1.MembershipReport
	repaired      int32
	reconcileMeta *procmeshv1.MutationMeta
}

func (s *membershipNodeStub) CheckMembership(context.Context, *connect.Request[procmeshv1.CheckMembershipRequest]) (*connect.Response[procmeshv1.CheckMembershipResponse], error) {
	return connect.NewResponse(&procmeshv1.CheckMembershipResponse{Report: s.report}), nil
}

func (s *membershipNodeStub) ReconcileMembership(_ context.Context, req *connect.Request[procmeshv1.ReconcileMembershipRequest]) (*connect.Response[procmeshv1.ReconcileMembershipResponse], error) {
	s.reconcileMeta = req.Msg.GetMeta()
	return connect.NewResponse(&procmeshv1.ReconcileMembershipResponse{Report: s.report, Repaired: s.repaired}), nil
}

func newMembershipCLIServer(t *testing.T, stub *membershipNodeStub) string {
	t.Helper()
	path, handler := procmeshv1connect.NewNodeServiceHandler(stub)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestCLI_ClusterMembershipCheckPrintsDriftAndReturnsFailure(t *testing.T) {
	stub := &membershipNodeStub{report: &procmeshv1.MembershipReport{
		Status: "DRIFTED", Freshness: "LIVE", HasQuorum: true, Leader: true, ObservedUnixMs: 1_700_000_000_123,
		Issues: []*procmeshv1.MembershipIssue{{
			NodeId: "node-b", Kind: "MISSING_MEMBER", Repairable: true, MemberState: "ADMITTED",
		}},
	}}
	url := newMembershipCLIServer(t, stub)

	code, out, errOut := runCLI("--server", url, "cluster", "membership", "check")
	if code != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	for _, want := range []string{
		"status=DRIFTED\n",
		"freshness=LIVE\n",
		"has_quorum=true\n",
		"leader=true\n",
		"observed_unix_ms=1700000000123\n",
		"issues=1\n",
		"node-b\tMISSING_MEMBER\tADMITTED\t-\ttrue\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in stdout=%q", want, out)
		}
	}
	if !strings.Contains(errOut, "membership status DRIFTED") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestCLI_ClusterMembershipReconcileUsesMutationMeta(t *testing.T) {
	stub := &membershipNodeStub{
		report:   &procmeshv1.MembershipReport{Status: "CLEAN", Freshness: "LIVE", HasQuorum: true, Leader: true},
		repaired: 2,
	}
	url := newMembershipCLIServer(t, stub)

	code, out, errOut := runCLI("--server", url, "--operation-id", "op-membership-cli", "--operator", "root", "cluster", "membership", "reconcile")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if !strings.Contains(out, "repaired=2\n") || !strings.Contains(out, "status=CLEAN\n") {
		t.Fatalf("stdout=%q", out)
	}
	if stub.reconcileMeta.GetOperationId() != "op-membership-cli" || stub.reconcileMeta.GetOperator() != "root" {
		t.Fatalf("meta=%+v", stub.reconcileMeta)
	}
}

func TestCLI_ClusterMembershipUsage(t *testing.T) {
	if !strings.Contains(usageText, "cluster membership check") || !strings.Contains(usageText, "cluster membership reconcile") {
		t.Fatal("usage missing cluster membership commands")
	}
	code, _, errOut := runCLI("cluster", "membership")
	if code != 2 || !strings.Contains(errOut, "missing cluster membership subcommand") {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
}
