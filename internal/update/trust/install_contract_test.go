package trust

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestInstallerAndUpdaterEmbedTheSameTrustedKeys(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	installer, err := os.ReadFile(filepath.Join(repositoryRoot, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`(?m)^readonly trusted_key_registry='([^']+)'$`).FindSubmatch(installer)
	if len(match) != 2 {
		t.Fatal("scripts/install.sh has no canonical trusted_key_registry")
	}
	registry := bytes.TrimSpace(embeddedKeyRegistry)
	if !bytes.Equal(match[1], registry) {
		t.Fatalf("installer trusted keys differ from %s", filepath.Join("internal", "update", "trust", "trusted_keys.json"))
	}
}
