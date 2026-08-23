package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_InvalidLogFormatExitsTwo(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--data-dir", t.TempDir(), "--log-format", "yaml"}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "log format") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRun_InvalidLogLevelExitsTwo(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--data-dir", t.TempDir(), "--log-level", "trace"}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "log level") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRun_HelpExitsZero(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--help"}, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "log-format") || !strings.Contains(stderr.String(), "log-level") || !strings.Contains(stderr.String(), "break-glass-socket") || !strings.Contains(stderr.String(), "break-glass-group") || !strings.Contains(stderr.String(), "pprof-listen") {
		t.Fatalf("help missing logging flags: %q", stderr.String())
	}
}
