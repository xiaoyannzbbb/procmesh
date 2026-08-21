package api

import (
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/process"
)

func TestViewOf_StartedAtUnixMs(t *testing.T) {
	started := time.UnixMilli(1_700_000_005_000).In(time.FixedZone("UTC+8", 8*3600))
	view := ViewOf(process.ProcessSpec{ProcessID: "p1"}, []process.Instance{{
		InstanceID: "i1",
		StartedAt:  &started,
	}})
	if len(view.Instances) != 1 {
		t.Fatalf("instances=%d", len(view.Instances))
	}
	if got := view.Instances[0].GetStartedUnixMs(); got != 1_700_000_005_000 {
		t.Fatalf("started_unix_ms=%d", got)
	}
}

func TestViewOf_LastError(t *testing.T) {
	view := ViewOf(process.ProcessSpec{ProcessID: "p1"}, []process.Instance{{
		InstanceID: "i1",
		LastError:  "chdir /missing: no such file or directory",
	}})
	if len(view.Instances) != 1 {
		t.Fatalf("instances=%d", len(view.Instances))
	}
	if got := view.Instances[0].GetLastError(); got != "chdir /missing: no such file or directory" {
		t.Fatalf("last_error=%q", got)
	}
}

func TestViewOf_NilStartedAtZero(t *testing.T) {
	view := ViewOf(process.ProcessSpec{ProcessID: "p1"}, []process.Instance{{
		InstanceID: "i1",
	}})
	if len(view.Instances) != 1 {
		t.Fatalf("instances=%d", len(view.Instances))
	}
	if got := view.Instances[0].GetStartedUnixMs(); got != 0 {
		t.Fatalf("started_unix_ms=%d want 0", got)
	}
}

func TestSpecToProto_LogDirectoryRoundtrip(t *testing.T) {
	in := process.ProcessSpec{
		ProcessID: "p1",
		Name:      "web",
		Command:   "/bin/true",
		Log: process.LogPolicy{
			Directory:      "/var/log/myapp",
			RedirectStderr: true,
		},
	}
	got := ProtoToSpec(SpecToProto(in))
	if got.Log.Directory != "/var/log/myapp" {
		t.Fatalf("directory=%q", got.Log.Directory)
	}
	if !got.Log.RedirectStderr {
		t.Fatal("redirect_stderr=false")
	}
	view := ViewOf(in, []process.Instance{{
		InstanceID:     "p1:0",
		ActiveRevision: 1,
	}})
	if len(view.Instances) != 1 || view.Instances[0].GetLogPathPending() {
		t.Fatalf("ViewOf must not set log_path_pending: %+v", view.Instances)
	}
}
