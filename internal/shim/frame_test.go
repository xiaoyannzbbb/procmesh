package shim_test

import (
	"bytes"
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/shim"
	shimpb "github.com/qleelulu/procmesh/proto/shim/v1"
	"google.golang.org/protobuf/proto"
)

func TestFrame_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := shim.WriteFrame(&buf, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := shim.ReadFrame(&buf)
	if err != nil || string(got) != "hello" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestFrame_RejectsTooLarge(t *testing.T) {
	var buf bytes.Buffer
	if err := shim.WriteFrame(&buf, make([]byte, 16<<20+1)); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestFrame_ProtoRoundTrip(t *testing.T) {
	env := &shimpb.Envelope{
		Body: &shimpb.Envelope_Start{
			Start: &shimpb.StartRequest{
				Command:    "echo",
				Args:       []string{"hello"},
				Env:        map[string]string{"FOO": "bar"},
				Cwd:        "/tmp",
				RunAsUser:  "nobody",
				StdoutPath: "/tmp/out",
				StderrPath: "/tmp/err",
			},
		},
	}
	payload, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := shim.WriteFrame(&buf, payload); err != nil {
		t.Fatal(err)
	}
	got, err := shim.ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var decoded shimpb.Envelope
	if err := proto.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	start := decoded.GetStart()
	if start == nil || start.Command != "echo" || start.GetEnv()["FOO"] != "bar" {
		t.Fatalf("got %+v", decoded)
	}
}
