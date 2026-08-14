package control

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argon2Time    = 1
	argon2Memory  = 65536
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 16
)

const passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// HashPassword returns an argon2id encoding:
// argon2id$v=19$m=65536,t=1,p=4$<saltB64>$<hashB64>
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(pw), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return fmt.Sprintf(
		"argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argon2Memory,
		argon2Time,
		argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword checks pw against an encoded argon2id hash.
func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	// argon2id $ v=19 $ m=...,t=...,p=... $ salt $ hash
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	if !strings.HasPrefix(parts[1], "v=") {
		return false
	}
	ver, err := strconv.Atoi(strings.TrimPrefix(parts[1], "v="))
	if err != nil || ver != argon2.Version {
		return false
	}
	var mem, timeCost uint32
	var threads uint8
	for _, kv := range strings.Split(parts[2], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return false
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return false
		}
		switch k {
		case "m":
			mem = uint32(n)
		case "t":
			timeCost = uint32(n)
		case "p":
			if n > 255 {
				return false
			}
			threads = uint8(n)
		default:
			return false
		}
	}
	if mem == 0 || timeCost == 0 || threads == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, timeCost, mem, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// RandomPassword returns n random characters from [A-Za-z0-9].
func RandomPassword(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("password length must be positive")
	}
	out := make([]byte, n)
	// rejection sampling keeps output uniform over alphabet
	const max = 256 - (256 % len(passwordAlphabet))
	for i := 0; i < n; {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("random password: %w", err)
		}
		if int(b[0]) >= max {
			continue
		}
		out[i] = passwordAlphabet[int(b[0])%len(passwordAlphabet)]
		i++
	}
	return string(out), nil
}
