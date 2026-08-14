package control

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

const (
	DefaultTokenTTL  = time.Hour
	DefaultTokenUses = 1

	tokensFileName = "tokens.json"
	tokensFileMode = 0o640
	tokenPlainN    = 32
	tokenPrefix    = "pmj_"
)

// TokenInfo is the public view of a stored join token (no hash/plaintext).
type TokenInfo struct {
	ID        string
	ExpiresAt time.Time
	Remaining int
	Revoked   bool
}

type tokenRecord struct {
	ID          string `json:"id"`
	Hash        string `json:"hash"`
	ExpiresUnix int64  `json:"expires_unix"`
	Remaining   int    `json:"remaining"`
	Revoked     bool   `json:"revoked"`
}

type tokensFile struct {
	Tokens []tokenRecord `json:"tokens"`
}

// CreateToken issues a join token under dir/tokens.json.
// ttl<=0 uses DefaultTokenTTL; uses<=0 uses DefaultTokenUses.
// Plaintext is returned once; only sha256 hex is stored.
func CreateToken(dir string, ttl time.Duration, uses int, now time.Time) (plaintext string, info TokenInfo, err error) {
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	if uses <= 0 {
		uses = DefaultTokenUses
	}

	var raw [tokenPlainN]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", TokenInfo{}, fmt.Errorf("generate token: %w", err)
	}
	plain := tokenPrefix + hex.EncodeToString(raw[:])
	hash := hashToken(plain)

	id, err := newUUID()
	if err != nil {
		return "", TokenInfo{}, err
	}
	expires := now.Add(ttl)
	rec := tokenRecord{
		ID:          id,
		Hash:        hash,
		ExpiresUnix: expires.Unix(),
		Remaining:   uses,
		Revoked:     false,
	}

	file, err := loadTokens(dir)
	if err != nil {
		return "", TokenInfo{}, err
	}
	file.Tokens = append(file.Tokens, rec)
	if err := saveTokens(dir, file); err != nil {
		return "", TokenInfo{}, err
	}

	return plain, TokenInfo{
		ID:        id,
		ExpiresAt: expires,
		Remaining: uses,
		Revoked:   false,
	}, nil
}

// ConsumeToken validates and decrements remaining uses for the matching token.
func ConsumeToken(dir, plaintext string, now time.Time) error {
	file, err := loadTokens(dir)
	if err != nil {
		return err
	}

	want := hashToken(plaintext)
	wantB := []byte(want)
	idx := -1
	for i := range file.Tokens {
		gotB := []byte(file.Tokens[i].Hash)
		if len(gotB) != len(wantB) {
			continue
		}
		if subtle.ConstantTimeCompare(gotB, wantB) == 1 {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errcode.E(errcode.INVALID, "invalid join token")
	}

	rec := &file.Tokens[idx]
	if rec.Revoked {
		return errcode.E(errcode.DENIED, "join token revoked")
	}
	if !now.Before(time.Unix(rec.ExpiresUnix, 0)) {
		return errcode.E(errcode.DENIED, "join token expired")
	}
	if rec.Remaining <= 0 {
		return errcode.E(errcode.DENIED, "join token exhausted")
	}

	rec.Remaining--
	if err := saveTokens(dir, file); err != nil {
		return err
	}
	return nil
}

// RevokeToken marks a token revoked by id. Missing id → NOT_FOUND.
func RevokeToken(dir, id string) error {
	file, err := loadTokens(dir)
	if err != nil {
		return err
	}
	for i := range file.Tokens {
		if file.Tokens[i].ID == id {
			file.Tokens[i].Revoked = true
			return saveTokens(dir, file)
		}
	}
	return errcode.E(errcode.NOT_FOUND, "join token not found")
}

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func tokensPath(dir string) string {
	return filepath.Join(dir, tokensFileName)
}

func loadTokens(dir string) (tokensFile, error) {
	path := tokensPath(dir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return tokensFile{Tokens: nil}, nil
		}
		return tokensFile{}, fmt.Errorf("read tokens: %w", err)
	}
	var file tokensFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return tokensFile{}, fmt.Errorf("parse tokens: %w", err)
	}
	if file.Tokens == nil {
		file.Tokens = []tokenRecord{}
	}
	return file, nil
}

func saveTokens(dir string, file tokensFile) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir tokens dir: %w", err)
	}
	path := tokensPath(dir)
	data, err := json.Marshal(file)
	if err != nil {
		return fmt.Errorf("marshal tokens: %w", err)
	}
	// temp + rename avoids half-written tokens.json
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, tokensFileMode); err != nil {
		return fmt.Errorf("write tokens tmp: %w", err)
	}
	if err := os.Chmod(tmp, tokensFileMode); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod tokens tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tokens: %w", err)
	}
	return nil
}
