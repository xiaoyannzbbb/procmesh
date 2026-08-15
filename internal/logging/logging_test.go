package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestNew_TextAndLevelFiltering(t *testing.T) {
	var out bytes.Buffer
	logger, err := New(&out, "text", "info")
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("hidden")
	logger.Info("agent started", "data_dir", "/data")
	got := out.String()
	if strings.Contains(got, "hidden") {
		t.Fatalf("debug leaked: %q", got)
	}
	if !strings.Contains(got, `level=INFO`) || !strings.Contains(got, `msg="agent started"`) || !strings.Contains(got, `data_dir=/data`) {
		t.Fatalf("text log missing fields: %q", got)
	}
}

func TestNew_JSONAndCaseInsensitiveValues(t *testing.T) {
	var out bytes.Buffer
	logger, err := New(&out, "JSON", "DEBUG")
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("http request", "status", 204)
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["level"] != "DEBUG" || got["msg"] != "http request" || got["status"] != float64(204) {
		t.Fatalf("json log = %#v", got)
	}
}

func TestNew_RejectsUnsupportedValues(t *testing.T) {
	for _, tc := range []struct {
		format, level, wantPrefix, wantValue string
	}{
		{"yaml", "info", "log format", "yaml"},
		{"text", "trace", "log level", "trace"},
	} {
		if _, err := New(io.Discard, tc.format, tc.level); err == nil {
			t.Fatalf("New(%q, %q) succeeded", tc.format, tc.level)
		} else if !strings.Contains(err.Error(), tc.wantPrefix) || !strings.Contains(err.Error(), tc.wantValue) {
			t.Fatalf("New(%q, %q) error = %q", tc.format, tc.level, err)
		}
	}
}

func TestNew_RejectsNilWriter(t *testing.T) {
	if _, err := New(nil, "text", "info"); err == nil {
		t.Fatal("New(nil, \"text\", \"info\") succeeded")
	}
}
