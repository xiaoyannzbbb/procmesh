package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/qleelulu/procmesh/internal/update/trust"
)

const maxExtractedBytes = 1 << 30

var allowedAncillaryFiles = map[string]struct{}{
	"README.md": {}, "agent.yaml": {}, "procmesh-agent.service": {}, "procmesh-agent-update@.service": {}, "procmesh-agent-update-recover.service": {},
}

type releaseMarker struct {
	SchemaVersion  int    `json:"schema_version"`
	ReleaseVersion string `json:"release_version"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ArtifactSHA256 string `json:"artifact_sha256"`
}

func stageVersion(operationDir, installRoot string, plan Plan, manifestDigest string, artifact trust.Artifact) (string, error) {
	versionsDir := filepath.Join(installRoot, "versions")
	if err := os.MkdirAll(versionsDir, 0o755); err != nil {
		return "", fmt.Errorf("create versions directory: %w", err)
	}
	finalDir := filepath.Join(versionsDir, plan.TargetVersion)
	marker, err := trust.CanonicalJSON(releaseMarker{
		SchemaVersion: 1, ReleaseVersion: plan.TargetVersion,
		ManifestSHA256: manifestDigest, ArtifactSHA256: artifact.SHA256,
	})
	if err != nil {
		return "", err
	}
	if existing, err := os.ReadFile(filepath.Join(finalDir, markerFilename)); err == nil {
		if bytes.Equal(existing, marker) {
			return finalDir, nil
		}
		return "", invalid("target version directory belongs to another release", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", invalid("inspect target version directory", err)
	} else if _, statErr := os.Stat(finalDir); statErr == nil {
		return "", invalid("target version directory is incomplete", nil)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", invalid("inspect target version directory", statErr)
	}

	stagingDir := filepath.Join(versionsDir, "."+plan.TargetVersion+"-"+plan.OperationID+".staging")
	if err := os.RemoveAll(stagingDir); err != nil {
		return "", err
	}
	if err := os.Mkdir(stagingDir, 0o700); err != nil {
		return "", err
	}
	if err := extractArchive(filepath.Join(operationDir, ArtifactFilename), stagingDir, artifact); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stagingDir, markerFilename), marker, 0o444); err != nil {
		return "", err
	}
	if err := syncTree(stagingDir); err != nil {
		return "", err
	}
	if err := os.Chmod(stagingDir, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(stagingDir, finalDir); err != nil {
		return "", err
	}
	if err := syncDir(versionsDir); err != nil {
		return "", err
	}
	return finalDir, nil
}

func extractArchive(archiveName, destination string, artifact trust.Artifact) error {
	file, err := os.Open(archiveName)
	if err != nil {
		return invalid("open release archive", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return invalid("open compressed release archive", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	prefix := strings.TrimSuffix(artifact.URL, ".tar.gz")
	required := make(map[string]bool, len(RequiredBinaries))
	for _, binary := range RequiredBinaries {
		required[binary] = false
	}
	var extracted int64
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return invalid("read release archive", err)
		}
		entryName := header.Name
		if header.Typeflag == tar.TypeDir && strings.HasSuffix(entryName, "/") {
			entryName = strings.TrimSuffix(entryName, "/")
		}
		if err := validateArchivePath(entryName); err != nil {
			return invalid("unsafe archive path", err)
		}
		clean := path.Clean(entryName)
		if clean == prefix && header.Typeflag == tar.TypeDir {
			continue
		}
		parts := strings.Split(clean, "/")
		if len(parts) != 2 || parts[0] != prefix {
			return invalid(fmt.Sprintf("archive entry %q is outside its release directory", clean), nil)
		}
		name := parts[1]
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return invalid("archive contains a non-regular file", nil)
		}
		if header.Mode&0o7000 != 0 || header.Size < 0 || extracted+header.Size > maxExtractedBytes {
			return invalid("archive entry mode or size is unsafe", nil)
		}
		if _, ancillary := allowedAncillaryFiles[name]; ancillary {
			if header.Mode&0o111 != 0 {
				return invalid("ancillary archive file must not be executable", nil)
			}
			if _, err := io.Copy(io.Discard, io.LimitReader(tarReader, header.Size)); err != nil {
				return invalid("read ancillary archive file", err)
			}
			extracted += header.Size
			continue
		}
		seen, expected := required[name]
		if !expected || seen || header.Size == 0 {
			return invalid("archive contains an unexpected or duplicate file", nil)
		}
		required[name] = true
		extracted += header.Size
		destinationName := filepath.Join(destination, name)
		output, err := os.OpenFile(destinationName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return invalid("create staged binary", err)
		}
		written, copyErr := io.Copy(output, io.LimitReader(tarReader, header.Size))
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil || written != header.Size || syncErr != nil || closeErr != nil {
			return invalid("write staged binary", errors.Join(copyErr, syncErr, closeErr))
		}
	}
	for binary, seen := range required {
		if !seen {
			return invalid("release archive is missing required binary "+binary, nil)
		}
	}
	return nil
}

func validateArchivePath(name string) error {
	if name == "" || strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name {
		return errors.New("non-canonical path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("path traversal")
		}
	}
	return nil
}

func syncTree(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return errors.New("staged version contains a directory")
		}
		file, err := os.Open(filepath.Join(directory, entry.Name()))
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	return syncDir(directory)
}
