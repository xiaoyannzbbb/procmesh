//go:build acceptance && web_e2e

package agent

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestP5_Playwright_LoginListFreshness409(t *testing.T) {
	if _, err := exec.LookPath("npx"); err != nil {
		t.Fatalf("npx required for P5 playwright")
	}

	addr, _ := startClusterAgent(t, "")
	code, out, errb := runP1CLI("--server", addr, "cluster", "init")
	if code != 0 {
		t.Fatalf("cluster init exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	pw := parseKV(out, "admin_password")
	if pw == "" {
		t.Fatalf("missing admin_password in %q", out)
	}

	waitClusterInited(t, addr)
	loginAdmin(t, addr, pw)

	spec := writeSleepSpec(t)
	code, out, errb = runP1CLI("--server", addr, "process", "apply", "--file", spec, "--expected-revision", "0")
	if code != 0 {
		t.Fatalf("apply exit=%d stderr=%q stdout=%q", code, errb, out)
	}
	waitGossipName(t, addr, "sleep")

	t.Setenv("PROCMESH_E2E_URL", "http://"+addr)
	t.Setenv("PROCMESH_E2E_USER", "admin")
	t.Setenv("PROCMESH_E2E_PASSWORD", pw)

	dir := playwrightWebDir(t)
	outb, err := runPlaywright(dir)
	if err != nil && playwrightMissingBrowser(outb) {
		install := exec.Command("npx", "playwright", "install", "chromium")
		install.Dir = dir
		if ib, ierr := install.CombinedOutput(); ierr != nil {
			t.Fatalf("npx playwright install chromium: %v\n%s", ierr, ib)
		}
		outb, err = runPlaywright(dir)
	}
	if err != nil {
		t.Fatalf("playwright test: %v\n%s", err, outb)
	}
}

func playwrightWebDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "web"))
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		t.Fatalf("web dir %s: %v", dir, err)
	}
	return dir
}

func runPlaywright(dir string) ([]byte, error) {
	cmd := exec.Command("npx", "playwright", "test")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

func playwrightMissingBrowser(out []byte) bool {
	return bytes.Contains(out, []byte("Executable doesn't exist")) ||
		bytes.Contains(out, []byte("browserType.launch"))
}
