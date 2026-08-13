//go:build darwin

package paths

func DefaultRoot() string {
	return "~/Library/Application Support/procmesh"
}
