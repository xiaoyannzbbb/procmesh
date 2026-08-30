package trust

import _ "embed"

//go:embed trusted_keys.json
var embeddedKeyRegistry []byte

// DefaultKeyring returns the release keys compiled into this ProcMesh build.
func DefaultKeyring() (Keyring, error) {
	return ParseKeyRegistry(embeddedKeyRegistry)
}
