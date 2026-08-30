package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func readVersionPointer(installRoot, name string) (string, error) {
	target, err := os.Readlink(filepath.Join(installRoot, name))
	if err != nil {
		return "", err
	}
	const prefix = "versions/"
	if len(target) <= len(prefix) || target[:len(prefix)] != prefix {
		return "", errors.New("version pointer target is not managed")
	}
	version := target[len(prefix):]
	if !versionPattern.MatchString(version) || target != filepath.ToSlash(filepath.Join(prefix, version)) {
		return "", errors.New("version pointer target is invalid")
	}
	info, err := os.Stat(filepath.Join(installRoot, target))
	if err != nil || !info.IsDir() {
		return "", errors.New("version pointer target does not exist")
	}
	return version, nil
}

func replaceVersionPointer(installRoot, name, version, operationID string) error {
	if name != "current" && name != "previous" || !versionPattern.MatchString(version) {
		return invalid("invalid version pointer", nil)
	}
	if info, err := os.Stat(filepath.Join(installRoot, "versions", version)); err != nil || !info.IsDir() {
		return invalid("version pointer target is missing", err)
	}
	temporary := filepath.Join(installRoot, "."+name+"-"+operationID+".tmp")
	_ = os.Remove(temporary)
	if err := os.Symlink(filepath.ToSlash(filepath.Join("versions", version)), temporary); err != nil {
		return fmt.Errorf("create %s pointer: %w", name, err)
	}
	if err := os.Rename(temporary, filepath.Join(installRoot, name)); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace %s pointer: %w", name, err)
	}
	return syncDir(installRoot)
}

func syncDir(name string) error {
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
