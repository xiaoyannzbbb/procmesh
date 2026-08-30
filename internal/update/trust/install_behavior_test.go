package trust

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestInstallerDownloaderRejectsUnsafeTransfersWithoutPublishingDestination(t *testing.T) {
	installer := installerPath(t)
	tests := []struct {
		name string
		fake string
	}{
		{
			name: "cross origin redirect",
			fake: `fake_response '302' 'https://example.com/release' ''`,
		},
		{
			name: "redirect loop",
			fake: `fake_response '302' 'https://github.com/xiaoyannzbbb/procmesh/releases/download/v1/file' ''`,
		},
		{
			name: "curl timeout",
			fake: `return 28`,
		},
		{
			name: "oversize response",
			fake: `fake_response '200' '' '0123456789abcdef'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "release")
			script := `
source "$INSTALLER"
fake_response() {
  local status=$1 location=$2 body=$3
  printf 'HTTP/1.1 %s response\r\n' "$status" >"$fake_headers"
  [[ -z "$location" ]] || printf 'Location: %s\r\n' "$location" >>"$fake_headers"
  printf '\r\n' >>"$fake_headers"
  printf '%s' "$body" >"$fake_output"
  printf '%s' "$status"
}
curl() {
  local fake_headers='' fake_output=''
  while (($#)); do
    case "$1" in
      --dump-header) fake_headers=$2; shift 2 ;;
      --output) fake_output=$2; shift 2 ;;
      --proto|--connect-timeout|--max-time|--retry|--max-filesize|--write-out) shift 2 ;;
      --*) shift ;;
      *) shift ;;
    esac
  done
  ` + tt.fake + `
}
download_file 'https://github.com/xiaoyannzbbb/procmesh/releases/download/v1/file' "$DESTINATION" 8
`
			command := exec.Command("bash", "-c", script)
			command.Env = append(os.Environ(), "INSTALLER="+installer, "DESTINATION="+destination)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("download_file succeeded: %s", output)
			}
			if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
				t.Fatalf("destination was published: %v", statErr)
			}
		})
	}
}

func TestInstallerRefusesExistingFlatOrCustomInstallationWithoutChangingIt(t *testing.T) {
	installer := installerPath(t)
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	managedRoot := filepath.Join(root, "lib", "procmesh")
	unit := filepath.Join(root, "procmesh-agent.service")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"procmesh", "procmesh-agent", "procmesh-shim"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("legacy "+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	unitContents := []byte("[Service]\nExecStart=/custom/procmesh-agent\nKillMode=control-group\n")
	if err := os.WriteFile(unit, unitContents, 0o644); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("bash", "-c", `source "$INSTALLER"; validate_flat_installation "$BIN_DIR" "$MANAGED_ROOT" "$UNIT" "$(id -u)"`)
	command.Env = append(os.Environ(), "INSTALLER="+installer, "BIN_DIR="+binDir, "MANAGED_ROOT="+managedRoot, "UNIT="+unit)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "automatic migration") {
		t.Fatalf("refusal err=%v output=%q", err, output)
	}
	for _, name := range []string{"procmesh", "procmesh-agent", "procmesh-shim"} {
		contents, readErr := os.ReadFile(filepath.Join(binDir, name))
		if readErr != nil || string(contents) != "legacy "+name {
			t.Fatalf("legacy %s changed: contents=%q err=%v", name, contents, readErr)
		}
	}
	contents, err := os.ReadFile(unit)
	if err != nil || string(contents) != string(unitContents) {
		t.Fatalf("unit changed: contents=%q err=%v", contents, err)
	}
}

func TestInstallerBootstrapsRecognizedFlatLayoutAndRollsBackHealthFailure(t *testing.T) {
	installer := installerPath(t)
	for _, tt := range []struct {
		name      string
		healthOK  bool
		wantError bool
	}{
		{name: "success", healthOK: true},
		{name: "health rollback", wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			managedRoot := filepath.Join(root, "lib", "procmesh")
			updateRoot := filepath.Join(root, "data", "update")
			unit := filepath.Join(root, "procmesh-agent.service")
			packageDir := filepath.Join(root, "package")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(packageDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"procmesh", "procmesh-agent", "procmesh-shim"} {
				if err := os.WriteFile(filepath.Join(binDir, name), []byte("legacy "+name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"procmesh", "procmesh-agent", "procmesh-shim", "procmesh-updater"} {
				if err := os.WriteFile(filepath.Join(packageDir, name), []byte("target "+name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"procmesh-agent-update@.service", "procmesh-agent-update-recover.service"} {
				if err := os.WriteFile(filepath.Join(packageDir, name), []byte("[Service]\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			unitContents := []byte("[Service]\nExecStart=" + binDir + "/procmesh-agent \\\n  --data-dir " + root + "/data \\\n  --config " + root + "/etc/agent.yaml \\\n  --listen 127.0.0.1:18680 \\\n  --shim-bin " + binDir + "/procmesh-shim\nKillMode=process\n")
			if err := os.WriteFile(unit, unitContents, 0o644); err != nil {
				t.Fatal(err)
			}

			script := `
source "$INSTALLER"
run_privileged() {
  if [[ "$1" == mv && "$2" == -Tf ]]; then
    command rm -f "$4"
    command mv -f "$3" "$4"
    return
  fi
  "$@"
}
systemctl() { printf '%s\n' "$*" >>"$SYSTEMCTL_LOG"; return 0; }
wait_for_update_health() { [[ "$HEALTH_OK" == 1 ]]; }
wait_for_legacy_health() { return 0; }
bootstrap_flat_installation "$BIN_DIR" "$MANAGED_ROOT" "$UPDATE_ROOT" "$UNIT" "$PACKAGE_DIR" v1.2.0 v1.2.1 "$(id -u)" "$UPDATE_UNIT" "$RECOVER_UNIT"
`
			command := exec.Command("bash", "-c", script)
			healthOK := "0"
			if tt.healthOK {
				healthOK = "1"
			}
			command.Env = append(os.Environ(),
				"INSTALLER="+installer, "BIN_DIR="+binDir, "MANAGED_ROOT="+managedRoot,
				"UPDATE_ROOT="+updateRoot, "UNIT="+unit, "PACKAGE_DIR="+packageDir,
				"HEALTH_OK="+healthOK, "SYSTEMCTL_LOG="+filepath.Join(root, "systemctl.log"),
				"UPDATE_UNIT="+filepath.Join(root, "procmesh-agent-update@.service"),
				"RECOVER_UNIT="+filepath.Join(root, "procmesh-agent-update-recover.service"),
			)
			output, err := command.CombinedOutput()
			if (err != nil) != tt.wantError {
				t.Fatalf("bootstrap err=%v output=%s", err, output)
			}
			legacyAgent, readErr := os.ReadFile(filepath.Join(managedRoot, "versions", "v1.2.0", "procmesh-agent"))
			if readErr != nil || string(legacyAgent) != "legacy procmesh-agent" {
				t.Fatalf("legacy agent=%q err=%v", legacyAgent, readErr)
			}
			wantCurrent := "versions/v1.2.1"
			if tt.wantError {
				wantCurrent = "versions/v1.2.0"
			}
			current, readErr := os.Readlink(filepath.Join(managedRoot, "current"))
			if readErr != nil || current != wantCurrent {
				t.Fatalf("current=%q want=%q err=%v", current, wantCurrent, readErr)
			}
			gotUnit, readErr := os.ReadFile(unit)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tt.wantError {
				if string(gotUnit) != string(unitContents) {
					t.Fatalf("legacy unit not restored: %q", gotUnit)
				}
			} else if !strings.Contains(string(gotUnit), managedRoot+"/current/procmesh-agent") || !strings.Contains(string(gotUnit), "KillMode=process") {
				t.Fatalf("managed unit not installed: %q", gotUnit)
			}
		})
	}
}

func TestInstallerBootstrapStagingFailureDoesNotPublishManagedLayout(t *testing.T) {
	installer := installerPath(t)
	for failAt := 1; failAt <= 13; failAt++ {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			root := t.TempDir()
			binDir, managedRoot, updateRoot, unit, packageDir := writeFlatBootstrapFixture(t, root)
			legacyAgent, err := os.ReadFile(filepath.Join(binDir, "procmesh-agent"))
			if err != nil {
				t.Fatal(err)
			}
			legacyUnit, err := os.ReadFile(unit)
			if err != nil {
				t.Fatal(err)
			}

			script := `
source "$INSTALLER"
write_count=0
run_privileged() {
  if [[ "$1" == sync ]]; then
    "$@"
    return
  fi
  ((write_count += 1))
  if [[ "$write_count" == "$FAIL_AT" ]]; then
    return 73
  fi
  if [[ "$1" == mv && "$2" == -Tf ]]; then
    command rm -f "$4"
    command mv -f "$3" "$4"
    return
  fi
  "$@"
}
systemctl() { return 0; }
wait_for_update_health() { return 0; }
wait_for_legacy_health() { return 0; }
bootstrap_flat_installation "$BIN_DIR" "$MANAGED_ROOT" "$UPDATE_ROOT" "$UNIT" "$PACKAGE_DIR" v1.2.0 v1.2.1 "$(id -u)" "$UPDATE_UNIT" "$RECOVER_UNIT"
`
			command := exec.Command("bash", "-c", script)
			command.Env = append(os.Environ(),
				"INSTALLER="+installer, "BIN_DIR="+binDir, "MANAGED_ROOT="+managedRoot,
				"UPDATE_ROOT="+updateRoot, "UNIT="+unit, "PACKAGE_DIR="+packageDir,
				"FAIL_AT="+strconv.Itoa(failAt),
				"UPDATE_UNIT="+filepath.Join(root, "procmesh-agent-update@.service"),
				"RECOVER_UNIT="+filepath.Join(root, "procmesh-agent-update-recover.service"),
			)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("bootstrap staging failure %d was ignored: %s", failAt, output)
			}
			if _, statErr := os.Stat(managedRoot); !os.IsNotExist(statErr) {
				t.Fatalf("failed staging %d published managed root: %v", failAt, statErr)
			}
			gotAgent, readErr := os.ReadFile(filepath.Join(binDir, "procmesh-agent"))
			if readErr != nil || string(gotAgent) != string(legacyAgent) {
				t.Fatalf("legacy agent changed: contents=%q err=%v", gotAgent, readErr)
			}
			gotUnit, readErr := os.ReadFile(unit)
			if readErr != nil || string(gotUnit) != string(legacyUnit) {
				t.Fatalf("legacy unit changed: contents=%q err=%v", gotUnit, readErr)
			}
		})
	}
}

func TestInstallerBootstrapRollbackRequiresLegacyHealth(t *testing.T) {
	installer := installerPath(t)
	root := t.TempDir()
	binDir, managedRoot, updateRoot, unit, packageDir := writeFlatBootstrapFixture(t, root)
	healthLog := filepath.Join(root, "health.log")

	script := `
source "$INSTALLER"
run_privileged() {
  if [[ "$1" == mv && "$2" == -Tf ]]; then
    command rm -f "$4"
    command mv -f "$3" "$4"
    return
  fi
  "$@"
}
systemctl() { return 0; }
wait_for_update_health() {
  return 1
}
wait_for_legacy_health() {
  printf '%s\n' "$2" >>"$HEALTH_LOG"
  return 0
}
bootstrap_flat_installation "$BIN_DIR" "$MANAGED_ROOT" "$UPDATE_ROOT" "$UNIT" "$PACKAGE_DIR" v1.2.0 v1.2.1 "$(id -u)" "$UPDATE_UNIT" "$RECOVER_UNIT"
`
	command := exec.Command("bash", "-c", script)
	command.Env = append(os.Environ(),
		"INSTALLER="+installer, "BIN_DIR="+binDir, "MANAGED_ROOT="+managedRoot,
		"UPDATE_ROOT="+updateRoot, "UNIT="+unit, "PACKAGE_DIR="+packageDir,
		"HEALTH_LOG="+healthLog,
		"UPDATE_UNIT="+filepath.Join(root, "procmesh-agent-update@.service"),
		"RECOVER_UNIT="+filepath.Join(root, "procmesh-agent-update-recover.service"),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("target health failure must report bootstrap failure: %s", output)
	}
	log, readErr := os.ReadFile(healthLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := filepath.Join(managedRoot, "versions", "v1.2.0", "procmesh-agent")
	if !strings.Contains(string(log), want) {
		t.Fatalf("rollback did not verify the legacy agent identity and health: %q", log)
	}
}

func TestInstallerLegacyRollbackHealthDoesNotRequireUpdateEndpoint(t *testing.T) {
	installer := installerPath(t)
	script := `
source "$INSTALLER"
curl() {
  case "${!#}" in
    */healthz|*/readyz) printf 'ok' ;;
    */updatez) return 22 ;;
    *) return 22 ;;
  esac
}
systemctl() { printf '4242\n'; }
readlink() { printf '%s\n' "$LEGACY_AGENT"; }
sleep() { return 0; }
wait_for_legacy_health 127.0.0.1:18680 "$LEGACY_AGENT"
`
	command := exec.Command("bash", "-c", script)
	command.Env = append(os.Environ(), "INSTALLER="+installer, "LEGACY_AGENT=/managed/versions/v1.2.0/procmesh-agent")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("legacy rollback health rejected a pre-U0 agent without /updatez: err=%v output=%s", err, output)
	}
}

func writeFlatBootstrapFixture(t *testing.T, root string) (binDir, managedRoot, updateRoot, unit, packageDir string) {
	t.Helper()
	binDir = filepath.Join(root, "bin")
	managedRoot = filepath.Join(root, "lib", "procmesh")
	updateRoot = filepath.Join(root, "data", "update")
	unit = filepath.Join(root, "procmesh-agent.service")
	packageDir = filepath.Join(root, "package")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"procmesh", "procmesh-agent", "procmesh-shim"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("legacy "+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"procmesh", "procmesh-agent", "procmesh-shim", "procmesh-updater"} {
		if err := os.WriteFile(filepath.Join(packageDir, name), []byte("target "+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"procmesh-agent-update@.service", "procmesh-agent-update-recover.service"} {
		if err := os.WriteFile(filepath.Join(packageDir, name), []byte("[Service]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	contents := []byte("[Service]\nExecStart=" + binDir + "/procmesh-agent \\\n  --data-dir " + root + "/data \\\n  --config " + root + "/etc/agent.yaml \\\n  --listen 127.0.0.1:18680 \\\n  --shim-bin " + binDir + "/procmesh-shim\nKillMode=process\n")
	if err := os.WriteFile(unit, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	return binDir, managedRoot, updateRoot, unit, packageDir
}

func installerPath(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "scripts", "install.sh")
}
