package trust

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	command := exec.Command("bash", "-c", `source "$INSTALLER"; refuse_unsafe_existing_installation "$BIN_DIR" "$MANAGED_ROOT" "$UNIT"`)
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

func installerPath(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "scripts", "install.sh")
}
