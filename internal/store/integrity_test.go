package store

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestCheckIntegrityMessages(t *testing.T) {
	if err := checkIntegrityMessages([]string{"ok"}); err != nil {
		t.Fatal(err)
	}
	if err := checkIntegrityMessages(nil); !errcode.Is(err, errcode.DEGRADED) {
		t.Fatalf("empty: %v", err)
	}
	if err := checkIntegrityMessages([]string{"page 2 is never used"}); !errcode.Is(err, errcode.DEGRADED) {
		t.Fatalf("bad: %v", err)
	}
}
