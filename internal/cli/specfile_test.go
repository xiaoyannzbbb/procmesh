package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_YAMLRestartMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.yaml")
	yaml := "" +
		"name: nginx\n" +
		"command: /usr/sbin/nginx\n" +
		"args:\n" +
		"  - -g\n" +
		"  - daemon off;\n" +
		"restart:\n" +
		"  mode: always\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	spec, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.GetName() != "nginx" {
		t.Fatalf("name=%q", spec.GetName())
	}
	if spec.GetCommand() != "/usr/sbin/nginx" {
		t.Fatalf("command=%q", spec.GetCommand())
	}
	gotArgs := spec.GetArgs()
	if len(gotArgs) != 2 || gotArgs[0] != "-g" || gotArgs[1] != "daemon off;" {
		t.Fatalf("args=%q", gotArgs)
	}
	if spec.GetRestart() == nil || spec.GetRestart().GetMode() != "always" {
		t.Fatalf("restart=%+v", spec.GetRestart())
	}
}

func TestLoad_JSONSnakeCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.json")
	body := `{"process_id":"p1","name":"web","command":"/bin/sleep","args":["5"],"working_directory":"/tmp","stop_timeout_ms":15000}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.GetProcessId() != "p1" || spec.GetName() != "web" || spec.GetCommand() != "/bin/sleep" {
		t.Fatalf("%+v", spec)
	}
	if spec.GetWorkingDirectory() != "/tmp" || spec.GetStopTimeoutMs() != 15000 {
		t.Fatalf("paths/timeout %+v", spec)
	}
	if len(spec.GetArgs()) != 1 || spec.GetArgs()[0] != "5" {
		t.Fatalf("args=%q", spec.GetArgs())
	}
}

func TestLoad_LogDirectoryAndRedirect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.yaml")
	yaml := "" +
		"name: web\n" +
		"command: /bin/true\n" +
		"log:\n" +
		"  directory: /var/log/myapp\n" +
		"  redirect_stderr: true\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.GetLog() == nil {
		t.Fatal("log missing")
	}
	if spec.GetLog().GetDirectory() != "/var/log/myapp" {
		t.Fatalf("directory=%q", spec.GetLog().GetDirectory())
	}
	if !spec.GetLog().GetRedirectStderr() {
		t.Fatal("redirect_stderr=false")
	}
}

func TestLoad_WebGeneratedYAMLContract(t *testing.T) {
	spec, err := Load(filepath.Join("testdata", "web_process_config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	const aboveJSSafeInteger int64 = 9_007_199_254_740_993
	const maxInt64 int64 = 9_223_372_036_854_775_807
	int64Fields := []struct {
		name string
		got  int64
		want int64
	}{
		{"stop_timeout_ms", spec.GetStopTimeoutMs(), aboveJSSafeInteger},
		{"restart.retry_window_ms", spec.GetRestart().GetRetryWindowMs(), maxInt64},
		{"restart.backoff.initial_ms", spec.GetRestart().GetBackoff().GetInitialMs(), aboveJSSafeInteger},
		{"restart.backoff.max_ms", spec.GetRestart().GetBackoff().GetMaxMs(), maxInt64},
		{"health.initial_delay_ms", spec.GetHealth().GetInitialDelayMs(), aboveJSSafeInteger},
		{"health.interval_ms", spec.GetHealth().GetIntervalMs(), maxInt64},
		{"health.timeout_ms", spec.GetHealth().GetTimeoutMs(), aboveJSSafeInteger},
		{"health.restart_cooldown_ms", spec.GetHealth().GetRestartCooldownMs(), maxInt64},
		{"log.max_size", spec.GetLog().GetMaxSize(), aboveJSSafeInteger},
		{"log.max_age_seconds", spec.GetLog().GetMaxAgeSeconds(), maxInt64},
		{"resources.cpu_quota_millis", spec.GetResources().GetCpuQuotaMillis(), aboveJSSafeInteger},
		{"resources.memory_bytes", spec.GetResources().GetMemoryBytes(), maxInt64},
		{"resources.open_files", spec.GetResources().GetOpenFiles(), aboveJSSafeInteger},
		{"latest_revision", spec.GetLatestRevision(), maxInt64},
	}
	for _, field := range int64Fields {
		if field.got != field.want {
			t.Errorf("%s=%d, want %d", field.name, field.got, field.want)
		}
	}
	if spec.GetArgs()[0] != "9007199254740993" || spec.GetEnvironment()["LIMIT"] != "9223372036854775807" {
		t.Fatalf("numeric-looking strings changed type: args=%q environment=%q", spec.GetArgs(), spec.GetEnvironment())
	}
}
