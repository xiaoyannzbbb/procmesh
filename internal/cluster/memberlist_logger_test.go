package cluster

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestMemberlistLoggerMapsLevels(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bridge := newMemberlistLogger(logger)

	bridge.Print("[DEBUG] memberlist: debug detail")
	bridge.Print("[INFO] memberlist: sync complete")
	bridge.Print("[WARN] memberlist: UDP probes failed")
	bridge.Print("[ERR] memberlist: Failed to send gossip")

	got := out.String()
	for _, want := range []string{
		`level=DEBUG msg="memberlist: debug detail" source=memberlist`,
		`level=INFO msg="memberlist: sync complete" source=memberlist`,
		`level=WARN msg="memberlist: UDP probes failed" source=memberlist`,
		`level=ERROR msg="memberlist: Failed to send gossip" source=memberlist`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("memberlist bridge missing %q: %s", want, got)
		}
	}
}
