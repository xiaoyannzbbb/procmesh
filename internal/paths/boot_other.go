//go:build !linux && !darwin

package paths

func readBootID() string {
	return "unknown"
}
