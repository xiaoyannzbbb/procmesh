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
