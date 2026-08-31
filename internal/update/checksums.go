package update

import (
	"bufio"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
)

var requiredArches = []string{"linux/amd64", "linux/arm64", "linux/armv7"}

var archBySuffix = map[string]string{
	"linux_amd64.tar.gz": "linux/amd64",
	"linux_arm64.tar.gz": "linux/arm64",
	"linux_armv7.tar.gz": "linux/armv7",
}

// ParseChecksums extracts sha256 digests for linux amd64/arm64/armv7 assets
// matching procmesh_<versionWithoutV>_linux_<arch>.tar.gz.
func ParseChecksums(body, versionWithoutV string) (map[string]string, error) {
	versionWithoutV = StripV(strings.TrimSpace(versionWithoutV))
	if versionWithoutV == "" {
		return nil, errcode.E(errcode.INVALID, "version required for checksum parse")
	}
	prefix := "procmesh_" + versionWithoutV + "_"
	out := make(map[string]string, len(requiredArches))
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sum, name := fields[0], fields[len(fields)-1]
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, prefix)
		arch, ok := archBySuffix[suffix]
		if !ok {
			continue
		}
		out[arch] = strings.ToLower(sum)
	}
	if err := sc.Err(); err != nil {
		return nil, errcode.Wrap(errcode.INVALID, "checksums parse failed", err)
	}
	for _, arch := range requiredArches {
		if out[arch] == "" {
			return nil, errcode.E(errcode.INVALID, "missing checksum for "+arch)
		}
	}
	return out, nil
}
