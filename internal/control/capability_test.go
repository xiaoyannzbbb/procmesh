package control_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestAdmissionCapabilityStateLifecycle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	state := mustBootstrap(t, now)
	if err := state.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{
		NodeID: "node-a", CertSerial: "AA11", Status: control.MemberAdmitted,
	}), now); err != nil {
		t.Fatal(err)
	}
	if err := state.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{
		NodeID: "node-b", CertSerial: "BB22", Status: control.MemberAdmitted,
	}), now); err != nil {
		t.Fatal(err)
	}

	init := control.CapabilityInitBody{CAFingerprint: strings.Repeat("a", 64), Epoch: 1, NodeID: "node-a", CertSerial: "AA11"}
	if err := state.Apply(mustEncode(t, control.CmdCapabilityInit, init), now); err != nil {
		t.Fatal(err)
	}
	if state.AdmissionCapability.CAFingerprint != init.CAFingerprint || state.AdmissionCapability.Epoch != 1 {
		t.Fatalf("capability=%+v", state.AdmissionCapability)
	}
	if got := state.AdmissionCapability.Nodes["node-a"]; got.Status != control.CapabilityReady || got.CertSerial != "AA11" {
		t.Fatalf("initial node=%+v", got)
	}
	if err := state.Apply(mustEncode(t, control.CmdCapabilityInit, control.CapabilityInitBody{
		CAFingerprint: init.CAFingerprint, Epoch: init.Epoch, NodeID: "node-b", CertSerial: "BB22",
	}), now); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("second initialized node bypassed proof: %v", err)
	}

	prepare := control.CapabilityPrepareBody{
		OperationID: "op-promote-b", NodeID: "node-b", CertSerial: "BB22",
		CAFingerprint: init.CAFingerprint, Epoch: 1, LeaderTerm: 7,
		Nonce: "001122", ExpiresUnix: now.Add(time.Minute).Unix(),
	}
	if err := state.Apply(mustEncode(t, control.CmdCapabilityPrepare, prepare), now); err != nil {
		t.Fatal(err)
	}
	if err := state.Apply(mustEncode(t, control.CmdCapabilityPrepare, prepare), now.Add(time.Second)); err != nil {
		t.Fatalf("idempotent prepare: %v", err)
	}
	if got := state.AdmissionCapability.Nodes["node-b"]; got.Status != control.CapabilityPrepared || got.OperationID != prepare.OperationID {
		t.Fatalf("prepared node=%+v", got)
	}

	ready := control.CapabilityReadyBody(prepare)
	if err := state.Apply(mustEncode(t, control.CmdCapabilityReady, ready), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := state.Apply(mustEncode(t, control.CmdCapabilityReady, ready), now.Add(2*time.Minute)); err != nil {
		t.Fatalf("idempotent ready after expiry: %v", err)
	}
	if got := state.AdmissionCapability.Nodes["node-b"]; got.Status != control.CapabilityReady {
		t.Fatalf("ready node=%+v", got)
	}
}

func TestAdmissionCapabilityStateRejectsConflictsAndFencing(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	newState := func(t *testing.T) *control.State {
		t.Helper()
		state := mustBootstrap(t, now)
		if err := state.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: "leader", CertSerial: "AA", Status: control.MemberAdmitted}), now); err != nil {
			t.Fatal(err)
		}
		if err := state.Apply(mustEncode(t, control.CmdMemberPut, control.MemberPutBody{NodeID: "target", CertSerial: "BB", Status: control.MemberAdmitted}), now); err != nil {
			t.Fatal(err)
		}
		if err := state.Apply(mustEncode(t, control.CmdCapabilityInit, control.CapabilityInitBody{CAFingerprint: strings.Repeat("b", 64), Epoch: 3, NodeID: "leader", CertSerial: "AA"}), now); err != nil {
			t.Fatal(err)
		}
		return state
	}
	base := control.CapabilityPrepareBody{OperationID: "op", NodeID: "target", CertSerial: "BB", CAFingerprint: strings.Repeat("b", 64), Epoch: 3, LeaderTerm: 9, Nonce: "nonce", ExpiresUnix: now.Add(time.Minute).Unix()}
	tests := []struct {
		name   string
		mutate func(*control.CapabilityPrepareBody)
		code   errcode.Code
	}{
		{name: "expired", mutate: func(b *control.CapabilityPrepareBody) { b.ExpiresUnix = now.Unix() }, code: errcode.INVALID},
		{name: "wrong epoch", mutate: func(b *control.CapabilityPrepareBody) { b.Epoch = 4 }, code: errcode.CONFLICT},
		{name: "wrong fingerprint", mutate: func(b *control.CapabilityPrepareBody) { b.CAFingerprint = strings.Repeat("c", 64) }, code: errcode.CONFLICT},
		{name: "wrong certificate", mutate: func(b *control.CapabilityPrepareBody) { b.CertSerial = "CC" }, code: errcode.CONFLICT},
		{name: "missing term", mutate: func(b *control.CapabilityPrepareBody) { b.LeaderTerm = 0 }, code: errcode.INVALID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newState(t)
			body := base
			tt.mutate(&body)
			err := state.Apply(mustEncode(t, control.CmdCapabilityPrepare, body), now)
			if !errcode.Is(err, tt.code) {
				t.Fatalf("err=%v want=%s", err, tt.code)
			}
		})
	}

	state := newState(t)
	if err := state.Apply(mustEncode(t, control.CmdCapabilityPrepare, base), now); err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.Nonce = "different"
	if err := state.Apply(mustEncode(t, control.CmdCapabilityPrepare, changed), now); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("operation conflict err=%v", err)
	}
	wrongTerm := control.CapabilityReadyBody(base)
	wrongTerm.LeaderTerm++
	if err := state.Apply(mustEncode(t, control.CmdCapabilityReady, wrongTerm), now); !errcode.Is(err, errcode.CONFLICT) {
		t.Fatalf("term fence err=%v", err)
	}
}

func TestAdmissionCapabilitySnapshotContainsNoSecret(t *testing.T) {
	state := control.NewState()
	state.AdmissionCapability = control.AdmissionCapabilityState{
		CAFingerprint: strings.Repeat("d", 64), Epoch: 1,
		Nodes: map[string]control.AdmissionCapabilityNode{"node": {Status: control.CapabilityReady}},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "ca_key") {
		t.Fatalf("secret field in state: %s", raw)
	}
}
