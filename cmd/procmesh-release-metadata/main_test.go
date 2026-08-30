package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrivateKeyRequiresPrivateFilePermissions(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "release.key")
	encoded := []byte(base64.StdEncoding.EncodeToString(privateKey.Seed()))
	if err := os.WriteFile(name, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPrivateKey(name); err == nil {
		t.Fatal("loadPrivateKey() accepted group/world-readable key")
	}
	if err := os.Chmod(name, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadPrivateKey(name)
	if err != nil {
		t.Fatalf("loadPrivateKey() error = %v", err)
	}
	if !loaded.Equal(privateKey) {
		t.Fatal("loadPrivateKey() returned a different key")
	}
}

func TestLoadPrivateKeyAcceptsPrivatePKCS8PEM(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "release.pem")
	payload := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(name, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadPrivateKey(name)
	if err != nil {
		t.Fatalf("loadPrivateKey() error = %v", err)
	}
	if !loaded.Equal(privateKey) {
		t.Fatal("loadPrivateKey() returned a different key")
	}
}
