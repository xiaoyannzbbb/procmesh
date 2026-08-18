package process_test

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/process"
)

func TestLogPathPending(t *testing.T) {
	inst := process.Instance{ActiveRevision: 1}
	if process.LogPathPending(process.LogPolicy{}, process.LogPolicy{}, process.Instance{}) {
		t.Fatal("never started")
	}
	if process.LogPathPending(process.LogPolicy{Directory: "/var/log/a"}, process.LogPolicy{Directory: "/var/log/a"}, inst) {
		t.Fatal("same")
	}
	if !process.LogPathPending(process.LogPolicy{Directory: "/var/log/b"}, process.LogPolicy{Directory: "/var/log/a"}, inst) {
		t.Fatal("dir changed")
	}
	if !process.LogPathPending(process.LogPolicy{RedirectStderr: true}, process.LogPolicy{}, inst) {
		t.Fatal("redirect changed")
	}
}
