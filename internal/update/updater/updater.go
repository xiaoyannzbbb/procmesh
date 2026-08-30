// Package updater applies one signed ProcMesh release to a managed installation.
package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/update/trust"
)

const (
	PlanFilename              = "plan.json"
	ChannelFilename           = "stable.json"
	ChannelSignatureFilename  = "stable.json.sig"
	ManifestFilename          = "manifest.json"
	ManifestSignatureFilename = "manifest.json.sig"
	ArtifactFilename          = "artifact.tar.gz"
	journalFilename           = "journal.json"
	markerFilename            = ".procmesh-release"
	journalSchemaVersion      = 2
)

var (
	RequiredBinaries = []string{"procmesh", "procmesh-agent", "procmesh-shim", "procmesh-updater"}
	operationPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	versionPattern   = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	digestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)

	ErrInterrupted    = errors.New("updater interrupted at durable checkpoint")
	ErrRolledBack     = errors.New("agent health verification failed; update rolled back")
	ErrRollbackFailed = errors.New("agent health verification failed; rollback failed")
)

type Status string

const (
	StatusRunning        Status = "RUNNING"
	StatusSucceeded      Status = "SUCCEEDED"
	StatusRollingBack    Status = "ROLLING_BACK"
	StatusRolledBack     Status = "ROLLED_BACK"
	StatusRollbackFailed Status = "ROLLBACK_FAILED"
)

type Phase string

const (
	PhaseStaged    Phase = "STAGED"
	PhaseSwitched  Phase = "SWITCHED"
	PhaseRestarted Phase = "RESTARTED"
	// PhaseHealthChecking is an acceptance checkpoint after the first probe, not a durable journal phase.
	PhaseHealthChecking Phase = "HEALTH_CHECKING"
	PhaseHealthy        Phase = "HEALTHY"
)

type Plan struct {
	SchemaVersion          int    `json:"schema_version"`
	OperationID            string `json:"operation_id"`
	ExpectedCurrentVersion string `json:"expected_current_version"`
	TargetVersion          string `json:"target_version"`
	HealthAddress          string `json:"health_address,omitempty"`
	HealthTimeoutSeconds   int    `json:"health_timeout_seconds,omitempty"`
}

type Journal struct {
	SchemaVersion  int    `json:"schema_version"`
	OperationID    string `json:"operation_id"`
	Status         Status `json:"status"`
	Phase          Phase  `json:"phase,omitempty"`
	FromVersion    string `json:"from_version"`
	TargetVersion  string `json:"target_version"`
	VerifiedAt     string `json:"verified_at,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
}

type Result struct {
	OperationID string
	Status      Status
	FromVersion string
	Version     string
}

type AgentService interface {
	RestartAgent(context.Context) error
}

type HealthExpectation struct {
	Version          string
	AgentPath        string
	Address          string
	Timeout          time.Duration
	FirstProbeIssued func() error
}

type HealthChecker interface {
	Check(context.Context, HealthExpectation) error
}

type healthCheckpointError struct{ err error }

func (e healthCheckpointError) Error() string { return e.err.Error() }
func (e healthCheckpointError) Unwrap() error { return e.err }

type Options struct {
	OperationID         string
	InstallRoot         string
	DataRoot            string
	Keys                trust.Keyring
	OS                  string
	Arch                string
	ProtocolVersion     int
	ShimProtocolVersion int
	Now                 func() time.Time
	Service             AgentService
	Health              HealthChecker
	Checkpoint          func(Phase) error
}

func Execute(ctx context.Context, options Options) (Result, error) {
	if err := validateOptions(options); err != nil {
		return Result{}, err
	}
	operationDir := filepath.Join(options.DataRoot, "operations", options.OperationID)
	if err := requirePrivateOperationDir(operationDir); err != nil {
		return Result{}, invalid("invalid updater operation directory", err)
	}
	plan, err := readPlan(filepath.Join(operationDir, PlanFilename), options.OperationID)
	if err != nil {
		return Result{}, err
	}
	journal, err := readJournal(filepath.Join(operationDir, journalFilename), plan)
	if err != nil {
		return Result{}, err
	}
	result := resultFromJournal(journal)
	switch journal.Status {
	case StatusSucceeded:
		return result, nil
	case StatusRolledBack:
		return result, ErrRolledBack
	case StatusRollbackFailed:
		return result, ErrRollbackFailed
	case StatusRollingBack:
		return rollback(ctx, options, operationDir, plan, journal)
	}

	manifest, artifact, manifestDigest, err := verifyJournalIdentity(options, operationDir, plan, &journal)
	if err != nil {
		if journal.VerifiedAt != "" || journal.Phase != "" {
			return rollbackAfterFailure(ctx, options, operationDir, plan, journal, err)
		}
		return Result{}, err
	}
	_ = manifest
	_, err = stageVersion(operationDir, options.InstallRoot, plan, manifestDigest, artifact)
	if err != nil {
		return Result{}, err
	}
	if phaseRank(journal.Phase) < phaseRank(PhaseStaged) {
		journal.Phase = PhaseStaged
		if err := writeJournal(operationDir, journal); err != nil {
			return Result{}, err
		}
	}
	if err := checkpoint(options, PhaseStaged); err != nil {
		return Result{}, err
	}

	if phaseRank(journal.Phase) < phaseRank(PhaseSwitched) {
		if err := switchVersion(options.InstallRoot, plan); err != nil {
			return Result{}, err
		}
		journal.Phase = PhaseSwitched
		if err := writeJournal(operationDir, journal); err != nil {
			return Result{}, err
		}
	}
	if err := checkpoint(options, PhaseSwitched); err != nil {
		return Result{}, err
	}

	if phaseRank(journal.Phase) < phaseRank(PhaseRestarted) {
		if err := options.Service.RestartAgent(ctx); err != nil {
			return rollbackAfterFailure(ctx, options, operationDir, plan, journal, fmt.Errorf("restart target agent: %w", err))
		}
		journal.Phase = PhaseRestarted
		if err := writeJournal(operationDir, journal); err != nil {
			return Result{}, err
		}
	}
	if err := checkpoint(options, PhaseRestarted); err != nil {
		return Result{}, err
	}

	if phaseRank(journal.Phase) < phaseRank(PhaseHealthy) {
		expectation := healthExpectation(options.InstallRoot, plan.TargetVersion, plan)
		expectation.FirstProbeIssued = func() error {
			if err := checkpoint(options, PhaseHealthChecking); err != nil {
				return healthCheckpointError{err: err}
			}
			return nil
		}
		if err := options.Health.Check(ctx, expectation); err != nil {
			var checkpointError healthCheckpointError
			if errors.As(err, &checkpointError) {
				return Result{}, checkpointError.err
			}
			return rollbackAfterFailure(ctx, options, operationDir, plan, journal, err)
		}
		journal.Phase = PhaseHealthy
		if err := writeJournal(operationDir, journal); err != nil {
			return Result{}, err
		}
	}
	if err := checkpoint(options, PhaseHealthy); err != nil {
		return Result{}, err
	}

	journal.Status = StatusSucceeded
	if err := writeJournal(operationDir, journal); err != nil {
		return Result{}, err
	}
	return resultFromJournal(journal), nil
}

func validateOptions(options Options) error {
	if !operationPattern.MatchString(options.OperationID) || !filepath.IsAbs(options.InstallRoot) || !filepath.IsAbs(options.DataRoot) {
		return invalid("invalid updater options", nil)
	}
	if options.OS == "" || options.Arch == "" || options.ProtocolVersion <= 0 || options.ShimProtocolVersion <= 0 || options.Service == nil || options.Health == nil || len(options.Keys) == 0 {
		return invalid("incomplete updater options", nil)
	}
	return nil
}

func readPlan(name, expectedOperationID string) (Plan, error) {
	var plan Plan
	if err := readCanonicalPrivateJSON(name, &plan); err != nil {
		return Plan{}, invalid("invalid updater plan", err)
	}
	if plan.SchemaVersion != 1 || plan.OperationID != expectedOperationID || !versionPattern.MatchString(plan.ExpectedCurrentVersion) || !versionPattern.MatchString(plan.TargetVersion) || compareVersions(plan.TargetVersion, plan.ExpectedCurrentVersion) <= 0 {
		return Plan{}, invalid("invalid updater plan", nil)
	}
	if plan.HealthAddress != "" {
		if err := requireLoopbackAddress(plan.HealthAddress); err != nil {
			return Plan{}, invalid("invalid updater health address", err)
		}
	}
	if plan.HealthTimeoutSeconds != 0 && (plan.HealthTimeoutSeconds < 10 || plan.HealthTimeoutSeconds > 300) {
		return Plan{}, invalid("invalid updater health timeout", nil)
	}
	return plan, nil
}

func readJournal(name string, plan Plan) (Journal, error) {
	var journal Journal
	err := readCanonicalPrivateJSON(name, &journal)
	if errors.Is(err, os.ErrNotExist) {
		return Journal{
			SchemaVersion: journalSchemaVersion, OperationID: plan.OperationID, Status: StatusRunning,
			FromVersion: plan.ExpectedCurrentVersion, TargetVersion: plan.TargetVersion,
		}, nil
	}
	if err != nil {
		return Journal{}, invalid("invalid updater journal", err)
	}
	if journal.SchemaVersion != journalSchemaVersion || journal.OperationID != plan.OperationID || journal.FromVersion != plan.ExpectedCurrentVersion || journal.TargetVersion != plan.TargetVersion {
		return Journal{}, invalid("updater journal does not match plan", nil)
	}
	return journal, nil
}

func verifyJournalIdentity(options Options, operationDir string, plan Plan, journal *Journal) (trust.Manifest, trust.Artifact, string, error) {
	verifiedAt := optionNow(options)
	if journal.VerifiedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, journal.VerifiedAt)
		if err != nil || parsed.UTC().Format(time.RFC3339Nano) != journal.VerifiedAt || !digestPattern.MatchString(journal.ManifestSHA256) || !digestPattern.MatchString(journal.ArtifactSHA256) {
			return trust.Manifest{}, trust.Artifact{}, "", invalid("updater journal has invalid verified release identity", err)
		}
		verifiedAt = parsed
	}
	manifest, artifact, manifestDigest, err := verifyInputsAt(options, operationDir, plan, verifiedAt)
	if err != nil {
		return trust.Manifest{}, trust.Artifact{}, "", err
	}
	if journal.VerifiedAt == "" {
		journal.VerifiedAt = verifiedAt.UTC().Format(time.RFC3339Nano)
		journal.ManifestSHA256 = manifestDigest
		journal.ArtifactSHA256 = artifact.SHA256
		if err := writeJournal(operationDir, *journal); err != nil {
			return trust.Manifest{}, trust.Artifact{}, "", err
		}
	} else if subtle.ConstantTimeCompare([]byte(journal.ManifestSHA256), []byte(manifestDigest)) != 1 || subtle.ConstantTimeCompare([]byte(journal.ArtifactSHA256), []byte(artifact.SHA256)) != 1 {
		return trust.Manifest{}, trust.Artifact{}, "", invalid("verified release identity changed", nil)
	}
	return manifest, artifact, manifestDigest, nil
}

func optionNow(options Options) time.Time {
	if options.Now != nil {
		return options.Now().UTC()
	}
	return time.Now().UTC()
}

func verifyInputsAt(options Options, operationDir string, plan Plan, verifiedAt time.Time) (trust.Manifest, trust.Artifact, string, error) {
	channelBytes, err := readPrivateFile(filepath.Join(operationDir, ChannelFilename), 1<<20)
	if err != nil {
		return trust.Manifest{}, trust.Artifact{}, "", invalid("read channel metadata", err)
	}
	channelSignature, err := readPrivateFile(filepath.Join(operationDir, ChannelSignatureFilename), 4<<10)
	if err != nil {
		return trust.Manifest{}, trust.Artifact{}, "", invalid("read channel signature", err)
	}
	manifestBytes, err := readPrivateFile(filepath.Join(operationDir, ManifestFilename), 4<<20)
	if err != nil {
		return trust.Manifest{}, trust.Artifact{}, "", invalid("read release manifest", err)
	}
	manifestSignature, err := readPrivateFile(filepath.Join(operationDir, ManifestSignatureFilename), 4<<10)
	if err != nil {
		return trust.Manifest{}, trust.Artifact{}, "", invalid("read manifest signature", err)
	}
	verifier := trust.Verifier{Keys: options.Keys, Now: func() time.Time { return verifiedAt }}
	index, err := verifier.VerifyChannel(channelBytes, channelSignature)
	if err != nil {
		return trust.Manifest{}, trust.Artifact{}, "", err
	}
	if index.Release.Version != plan.TargetVersion {
		return trust.Manifest{}, trust.Artifact{}, "", invalid("channel target does not match updater plan", nil)
	}
	manifest, err := verifier.VerifyManifest(index, manifestBytes, manifestSignature)
	if err != nil {
		return trust.Manifest{}, trust.Artifact{}, "", err
	}
	if manifest.ProtocolVersion != options.ProtocolVersion || !containsInt(manifest.CompatibleFromProtocols, options.ProtocolVersion) || options.ShimProtocolVersion < manifest.ShimProtocolMin || options.ShimProtocolVersion > manifest.ShimProtocolMax || !containsString(manifest.RollbackSafeFrom, plan.ExpectedCurrentVersion) {
		return trust.Manifest{}, trust.Artifact{}, "", fmt.Errorf("release is not compatible with the running agent: %w", errcode.E(errcode.INCOMPATIBLE_VERSION, "release compatibility check failed"))
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.OS == options.OS && artifact.Arch == options.Arch {
			if err := verifyArtifactFile(filepath.Join(operationDir, ArtifactFilename), artifact); err != nil {
				return trust.Manifest{}, trust.Artifact{}, "", err
			}
			return manifest, artifact, index.Release.ManifestSHA256, nil
		}
	}
	return trust.Manifest{}, trust.Artifact{}, "", invalid("release has no artifact for this platform", nil)
}

func verifyArtifactFile(name string, artifact trust.Artifact) error {
	file, err := os.Open(name)
	if err != nil {
		return invalid("read staged artifact", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return invalid("stat staged artifact", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() != artifact.Size {
		return invalid("staged artifact size or permissions do not match manifest", nil)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return invalid("hash staged artifact", err)
	}
	expected, _ := hex.DecodeString(artifact.SHA256)
	if subtle.ConstantTimeCompare(hash.Sum(nil), expected) != 1 {
		return invalid("staged artifact digest does not match manifest", nil)
	}
	return nil
}

func switchVersion(installRoot string, plan Plan) error {
	current, err := readVersionPointer(installRoot, "current")
	if err != nil {
		return invalid("read current version pointer", err)
	}
	if current == plan.TargetVersion {
		previous, err := readVersionPointer(installRoot, "previous")
		if err != nil || previous != plan.ExpectedCurrentVersion {
			return invalid("current and previous pointers are inconsistent", err)
		}
		return nil
	}
	if current != plan.ExpectedCurrentVersion {
		return fmt.Errorf("current agent version changed: %w", errcode.E(errcode.CONFLICT, "expected current version mismatch"))
	}
	if err := replaceVersionPointer(installRoot, "previous", current, plan.OperationID); err != nil {
		return err
	}
	return replaceVersionPointer(installRoot, "current", plan.TargetVersion, plan.OperationID)
}

func rollbackAfterFailure(ctx context.Context, options Options, operationDir string, plan Plan, journal Journal, _ error) (Result, error) {
	journal.Status = StatusRollingBack
	if err := writeJournal(operationDir, journal); err != nil {
		return Result{}, err
	}
	return rollback(ctx, options, operationDir, plan, journal)
}

func rollback(ctx context.Context, options Options, operationDir string, plan Plan, journal Journal) (Result, error) {
	journal.Status = StatusRollingBack
	if err := replaceVersionPointer(options.InstallRoot, "current", plan.ExpectedCurrentVersion, plan.OperationID); err != nil {
		journal.Status = StatusRollbackFailed
		_ = writeJournal(operationDir, journal)
		return resultFromJournal(journal), fmt.Errorf("restore previous version pointer: %w", ErrRollbackFailed)
	}
	if err := options.Service.RestartAgent(ctx); err != nil {
		journal.Status = StatusRollbackFailed
		_ = writeJournal(operationDir, journal)
		return resultFromJournal(journal), fmt.Errorf("restart previous agent: %w", ErrRollbackFailed)
	}
	expectation := healthExpectation(options.InstallRoot, plan.ExpectedCurrentVersion, plan)
	if err := options.Health.Check(ctx, expectation); err != nil {
		journal.Status = StatusRollbackFailed
		_ = writeJournal(operationDir, journal)
		return resultFromJournal(journal), fmt.Errorf("previous agent did not recover: %w", ErrRollbackFailed)
	}
	journal.Status = StatusRolledBack
	if err := writeJournal(operationDir, journal); err != nil {
		return Result{}, err
	}
	return resultFromJournal(journal), ErrRolledBack
}

func checkpoint(options Options, phase Phase) error {
	if options.Checkpoint == nil {
		return nil
	}
	return options.Checkpoint(phase)
}

func phaseRank(phase Phase) int {
	switch phase {
	case PhaseStaged:
		return 1
	case PhaseSwitched:
		return 2
	case PhaseRestarted:
		return 3
	case PhaseHealthy:
		return 4
	default:
		return 0
	}
}

func resultFromJournal(journal Journal) Result {
	version := journal.TargetVersion
	if journal.Status == StatusRolledBack {
		version = journal.FromVersion
	} else if journal.Status == StatusRollbackFailed {
		version = ""
	}
	return Result{OperationID: journal.OperationID, Status: journal.Status, FromVersion: journal.FromVersion, Version: version}
}

func healthExpectation(installRoot, releaseVersion string, plan Plan) HealthExpectation {
	return HealthExpectation{
		Version: releaseVersion, AgentPath: filepath.Join(installRoot, "versions", releaseVersion, "procmesh-agent"),
		Address: plan.HealthAddress, Timeout: time.Duration(plan.HealthTimeoutSeconds) * time.Second,
	}
}

// RecoverAll resumes every durable operation in the fixed data root.
func RecoverAll(ctx context.Context, options Options) error {
	operationsDir := filepath.Join(options.DataRoot, "operations")
	entries, err := os.ReadDir(operationsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var recoveryErrors []error
	for _, entry := range entries {
		if !entry.IsDir() || !operationPattern.MatchString(entry.Name()) {
			recoveryErrors = append(recoveryErrors, invalid("unexpected entry in updater operations directory", nil))
			continue
		}
		operationOptions := options
		operationOptions.OperationID = entry.Name()
		_, executeErr := Execute(ctx, operationOptions)
		if executeErr != nil && !errors.Is(executeErr, ErrRolledBack) {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("recover operation %s: %w", entry.Name(), executeErr))
		}
	}
	return errors.Join(recoveryErrors...)
}

func requirePrivateOperationDir(name string) error {
	info, err := os.Stat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("operation directory must be root-private")
	}
	return nil
}

func readCanonicalPrivateJSON(name string, destination any) error {
	payload, err := readPrivateFile(name, 4<<20)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	canonical, err := trust.CanonicalJSON(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, canonical) {
		return errors.New("JSON is not canonical")
	}
	return nil
}

func readPrivateFile(name string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxBytes {
		return nil, errors.New("file must be private, regular, and within size limit")
	}
	return io.ReadAll(io.LimitReader(file, maxBytes+1))
}

func writeJournal(operationDir string, journal Journal) error {
	payload, err := trust.CanonicalJSON(journal)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(operationDir, ".journal-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filepath.Join(operationDir, journalFilename)); err != nil {
		return err
	}
	return syncDir(operationDir)
}

func invalid(message string, cause error) error {
	if cause == nil {
		return errcode.E(errcode.INVALID, message)
	}
	return errcode.Wrap(errcode.INVALID, message, cause)
}

func compareVersions(left, right string) int {
	leftParts := strings.Split(strings.TrimPrefix(left, "v"), ".")
	rightParts := strings.Split(strings.TrimPrefix(right, "v"), ".")
	for index := range leftParts {
		leftValue, _ := strconv.ParseUint(leftParts[index], 10, 64)
		rightValue, _ := strconv.ParseUint(rightParts[index], 10, 64)
		if leftValue < rightValue {
			return -1
		}
		if leftValue > rightValue {
			return 1
		}
	}
	return 0
}

func containsInt(values []int, expected int) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
