package agent

import "testing"

func TestAgent_InitClosesUnauth(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	code, out, errb := runP1CLI("--server", addr, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	code, out, errb = runP1CLI("--server", addr, "process", "list")
	if code == 0 {
		t.Fatalf("unauth process list should fail after init, stdout=%q", out)
	}
}
