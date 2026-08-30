package updater_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/update/releasemeta"
	"github.com/qleelulu/procmesh/internal/update/trust"
	"github.com/qleelulu/procmesh/internal/update/updater"
)

const operationID = "018f47a2-9c4e-7b1a-8f3d-123456789abc"

type fakeService struct{ restarts int }

func (s *fakeService) RestartAgent(context.Context) error {
	s.restarts++
	return nil
}

type healthFunc func(context.Context, updater.HealthExpectation) error

func (f healthFunc) Check(ctx context.Context, expectation updater.HealthExpectation) error {
	return f(ctx, expectation)
}

func TestExecuteAtomicallySwitchesSignedRelease(t *testing.T) {
	fixture := newFixture(t, safeArchiveEntries())
	service := &fakeService{}
	result, err := updater.Execute(context.Background(), fixture.options(service, healthy))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Status != updater.StatusSucceeded {
		t.Fatalf("status = %q", result.Status)
	}
	if got := readLink(t, filepath.Join(fixture.installRoot, "current")); got != "versions/v1.2.1" {
		t.Fatalf("current = %q", got)
	}
	if got := readLink(t, filepath.Join(fixture.installRoot, "previous")); got != "versions/v1.2.0" {
		t.Fatalf("previous = %q", got)
	}
	if service.restarts != 1 {
		t.Fatalf("restarts = %d", service.restarts)
	}
	for _, binary := range updater.RequiredBinaries {
		info, err := os.Stat(filepath.Join(fixture.installRoot, "versions", "v1.2.1", binary))
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("installed %s: info=%v err=%v", binary, info, err)
		}
	}
}

func TestExecuteRejectsUnsafeArtifactBeforeSwitch(t *testing.T) {
	tests := []struct {
		name    string
		entries []archiveEntry
	}{
		{name: "path traversal", entries: append(safeArchiveEntries(), archiveEntry{name: "../escape", body: "bad", mode: 0o755})},
		{name: "symlink", entries: append(safeArchiveEntries(), archiveEntry{name: "procmesh_1.2.1_linux_amd64/link", mode: 0o777, typeflag: tar.TypeSymlink, linkname: "/etc/passwd"})},
		{name: "unexpected executable", entries: append(safeArchiveEntries(), archiveEntry{name: "procmesh_1.2.1_linux_amd64/post-install", body: "#!/bin/sh", mode: 0o755})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newFixture(t, tt.entries)
			service := &fakeService{}
			if _, err := updater.Execute(context.Background(), fixture.options(service, healthy)); err == nil {
				t.Fatal("Execute() error = nil")
			}
			if got := readLink(t, filepath.Join(fixture.installRoot, "current")); got != "versions/v1.2.0" {
				t.Fatalf("current = %q", got)
			}
			if service.restarts != 0 {
				t.Fatalf("restarts = %d", service.restarts)
			}
		})
	}
}

func TestExecuteRejectsArtifactDigestMismatchBeforeSwitch(t *testing.T) {
	fixture := newFixture(t, safeArchiveEntries())
	archivePath := filepath.Join(fixture.operationDir, updater.ArtifactFilename)
	file, err := os.OpenFile(archivePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("tampered"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{}

	if _, err := updater.Execute(context.Background(), fixture.options(service, healthy)); err == nil {
		t.Fatal("Execute() error = nil")
	}
	if got := readLink(t, filepath.Join(fixture.installRoot, "current")); got != "versions/v1.2.0" {
		t.Fatalf("current = %q", got)
	}
	if service.restarts != 0 {
		t.Fatalf("restarts = %d", service.restarts)
	}
}

func TestExecuteRejectsInvalidReleaseSignatureBeforeSwitch(t *testing.T) {
	fixture := newFixture(t, safeArchiveEntries())
	signaturePath := filepath.Join(fixture.operationDir, updater.ManifestSignatureFilename)
	signature, err := os.ReadFile(signaturePath)
	if err != nil {
		t.Fatal(err)
	}
	signature[len(signature)-2] ^= 1
	writeFile(t, signaturePath, signature, 0o600)
	service := &fakeService{}
	if _, err := updater.Execute(context.Background(), fixture.options(service, healthy)); err == nil {
		t.Fatal("Execute() error = nil")
	}
	if service.restarts != 0 || readLink(t, filepath.Join(fixture.installRoot, "current")) != "versions/v1.2.0" {
		t.Fatal("invalid signature caused a side effect")
	}
}

func TestExecuteRejectsUnsafePlanAndIncompatibleReleaseBeforeSwitch(t *testing.T) {
	t.Run("non-loopback health address", func(t *testing.T) {
		fixture := newFixture(t, safeArchiveEntries())
		writePlan(t, fixture.operationDir, updater.Plan{
			SchemaVersion: 1, OperationID: operationID, ExpectedCurrentVersion: "v1.2.0", TargetVersion: "v1.2.1",
			HealthAddress: "192.0.2.10:18680", HealthTimeoutSeconds: 30,
		})
		service := &fakeService{}
		if _, err := updater.Execute(context.Background(), fixture.options(service, healthy)); err == nil {
			t.Fatal("Execute() error = nil")
		}
		if service.restarts != 0 || readLink(t, filepath.Join(fixture.installRoot, "current")) != "versions/v1.2.0" {
			t.Fatal("unsafe plan caused a side effect")
		}
	})

	t.Run("protocol mismatch", func(t *testing.T) {
		fixture := newFixture(t, safeArchiveEntries())
		service := &fakeService{}
		options := fixture.options(service, healthy)
		options.ProtocolVersion = 2
		if _, err := updater.Execute(context.Background(), options); err == nil {
			t.Fatal("Execute() error = nil")
		}
		if service.restarts != 0 || readLink(t, filepath.Join(fixture.installRoot, "current")) != "versions/v1.2.0" {
			t.Fatal("incompatible release caused a side effect")
		}
	})
}

func TestExecuteRollsBackWhenNewAgentIsNotHealthy(t *testing.T) {
	fixture := newFixture(t, safeArchiveEntries())
	service := &fakeService{}
	check := healthFunc(func(_ context.Context, expectation updater.HealthExpectation) error {
		if expectation.Version == "v1.2.1" {
			return errors.New("new agent not ready")
		}
		return nil
	})

	result, err := updater.Execute(context.Background(), fixture.options(service, check))
	if !errors.Is(err, updater.ErrRolledBack) {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Status != updater.StatusRolledBack {
		t.Fatalf("status = %q", result.Status)
	}
	if got := readLink(t, filepath.Join(fixture.installRoot, "current")); got != "versions/v1.2.0" {
		t.Fatalf("current = %q", got)
	}
	if service.restarts != 2 {
		t.Fatalf("restarts = %d", service.restarts)
	}
}

func TestExecuteRecoversAfterInterruptionAtEveryDurablePhase(t *testing.T) {
	for _, phase := range []updater.Phase{updater.PhaseStaged, updater.PhaseSwitched, updater.PhaseRestarted, updater.PhaseHealthy} {
		t.Run(string(phase), func(t *testing.T) {
			fixture := newFixture(t, safeArchiveEntries())
			service := &fakeService{}
			options := fixture.options(service, healthy)
			interrupted := false
			options.Checkpoint = func(got updater.Phase) error {
				if got == phase && !interrupted {
					interrupted = true
					return updater.ErrInterrupted
				}
				return nil
			}
			if _, err := updater.Execute(context.Background(), options); !errors.Is(err, updater.ErrInterrupted) {
				t.Fatalf("first Execute() error = %v", err)
			}

			options.Checkpoint = nil
			result, err := updater.Execute(context.Background(), options)
			if err != nil || result.Status != updater.StatusSucceeded {
				t.Fatalf("recovered Execute() result=%#v err=%v", result, err)
			}
			if got := readLink(t, filepath.Join(fixture.installRoot, "current")); got != "versions/v1.2.1" {
				t.Fatalf("current = %q", got)
			}
		})
	}
}

func TestRecoverAllResumesInterruptedOperation(t *testing.T) {
	fixture := newFixture(t, safeArchiveEntries())
	service := &fakeService{}
	options := fixture.options(service, healthy)
	options.Checkpoint = func(phase updater.Phase) error {
		if phase == updater.PhaseSwitched {
			return updater.ErrInterrupted
		}
		return nil
	}
	if _, err := updater.Execute(context.Background(), options); !errors.Is(err, updater.ErrInterrupted) {
		t.Fatalf("Execute() error = %v", err)
	}
	options.OperationID = ""
	options.Checkpoint = nil
	if err := updater.RecoverAll(context.Background(), options); err != nil {
		t.Fatalf("RecoverAll() error = %v", err)
	}
	if got := readLink(t, filepath.Join(fixture.installRoot, "current")); got != "versions/v1.2.1" {
		t.Fatalf("current = %q", got)
	}
}

func TestExecuteDoesNotSignalUnrelatedBusinessProcess(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep command unavailable")
	}
	process := exec.Command("sleep", "30")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = process.Process.Kill()
		_, _ = process.Process.Wait()
	}()

	fixture := newFixture(t, safeArchiveEntries())
	if _, err := updater.Execute(context.Background(), fixture.options(&fakeService{}, healthy)); err != nil {
		t.Fatal(err)
	}
	if err := process.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("business process no longer alive: %v", err)
	}
}

var healthy = healthFunc(func(_ context.Context, expectation updater.HealthExpectation) error {
	if _, err := os.Stat(expectation.AgentPath); err != nil {
		return err
	}
	return nil
})

type fixture struct {
	installRoot  string
	dataRoot     string
	operationDir string
	keys         trust.Keyring
}

func newFixture(t *testing.T, entries []archiveEntry) fixture {
	t.Helper()
	root := t.TempDir()
	installRoot := filepath.Join(root, "lib", "procmesh")
	dataRoot := filepath.Join(root, "data", "update")
	operationDir := filepath.Join(dataRoot, "operations", operationID)
	oldVersionDir := filepath.Join(installRoot, "versions", "v1.2.0")
	if err := os.MkdirAll(oldVersionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, binary := range updater.RequiredBinaries {
		if err := os.WriteFile(filepath.Join(oldVersionDir, binary), []byte("old "+binary), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("versions/v1.2.0", filepath.Join(installRoot, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(operationDir, 0o700); err != nil {
		t.Fatal(err)
	}

	archiveDir := t.TempDir()
	archiveName := "procmesh_1.2.1_linux_amd64.tar.gz"
	archivePath := filepath.Join(archiveDir, archiveName)
	writeArchive(t, archivePath, entries)
	privateKey := testPrivateKey("updater")
	keys := trust.Keyring{"2026-release": privateKey.Public().(ed25519.PublicKey)}
	metadata, err := releasemeta.Generate(releasemeta.Options{
		Version: "v1.2.1", ArtifactDir: archiveDir, RollbackSafeFrom: []string{"v1.2.0"},
		ProtocolVersion: 1, ShimProtocolMin: 1, ShimProtocolMax: 1,
		KeyID: "2026-release", PrivateKey: privateKey, TrustedKeys: keys,
		Now: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC), IndexValidity: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(operationDir, updater.ChannelFilename), metadata.ChannelIndex, 0o600)
	writeFile(t, filepath.Join(operationDir, updater.ChannelSignatureFilename), metadata.ChannelSignature, 0o600)
	writeFile(t, filepath.Join(operationDir, updater.ManifestFilename), metadata.Manifest, 0o600)
	writeFile(t, filepath.Join(operationDir, updater.ManifestSignatureFilename), metadata.ManifestSignature, 0o600)
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(operationDir, updater.ArtifactFilename), archiveBytes, 0o600)
	planBytes, err := trust.CanonicalJSON(updater.Plan{
		SchemaVersion: 1, OperationID: operationID, ExpectedCurrentVersion: "v1.2.0", TargetVersion: "v1.2.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(operationDir, updater.PlanFilename), planBytes, 0o600)
	return fixture{installRoot: installRoot, dataRoot: dataRoot, operationDir: operationDir, keys: keys}
}

func (f fixture) options(service updater.AgentService, health updater.HealthChecker) updater.Options {
	return updater.Options{
		OperationID: operationID, InstallRoot: f.installRoot, DataRoot: f.dataRoot,
		Keys: f.keys, OS: "linux", Arch: "amd64", ProtocolVersion: 1, ShimProtocolVersion: 1, Now: func() time.Time {
			return time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC)
		}, Service: service, Health: health,
	}
}

type archiveEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	linkname string
}

func safeArchiveEntries() []archiveEntry {
	entries := []archiveEntry{{name: "procmesh_1.2.1_linux_amd64/", mode: 0o755, typeflag: tar.TypeDir}}
	for _, binary := range updater.RequiredBinaries {
		entries = append(entries, archiveEntry{name: "procmesh_1.2.1_linux_amd64/" + binary, body: "new " + binary, mode: 0o755})
	}
	return entries
}

func writeArchive(t *testing.T, destination string, entries []archiveEntry) {
	t.Helper()
	file, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: typeflag, Linkname: entry.linkname}
		if typeflag != tar.TypeReg && typeflag != tar.TypeRegA {
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func testPrivateKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("procmesh updater test " + label))
	return ed25519.NewKeyFromSeed(seed[:])
}

func writeFile(t *testing.T, name string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func writePlan(t *testing.T, operationDir string, plan updater.Plan) {
	t.Helper()
	payload, err := trust.CanonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(operationDir, updater.PlanFilename), payload, 0o600)
}

func readLink(t *testing.T, name string) string {
	t.Helper()
	target, err := os.Readlink(name)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func ExamplePlan() {
	plan, _ := trust.CanonicalJSON(updater.Plan{
		SchemaVersion: 1, OperationID: operationID, ExpectedCurrentVersion: "v1.2.0", TargetVersion: "v1.2.1",
	})
	fmt.Println(string(plan))
	// Output: {"schema_version":1,"operation_id":"018f47a2-9c4e-7b1a-8f3d-123456789abc","expected_current_version":"v1.2.0","target_version":"v1.2.1"}
}
