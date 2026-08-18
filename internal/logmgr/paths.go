package logmgr

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/paths"
	"golang.org/x/sys/unix"
)

var systemLogDirPrefixes = []string{
	"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64",
	"/boot", "/dev", "/proc", "/sys", "/root",
}

var agentInternalNames = []string{"store.db", "raft", "cluster", "runtime", "shim"}

func Resolve(layout paths.Layout, directory, processID, instanceID string, ordinal int) (stdout, stderr string) {
	var dir string
	if directory == "" {
		dir = filepath.Join(layout.LogDir, processID, instanceID)
	} else {
		dir = filepath.Join(directory, strconv.Itoa(ordinal))
	}
	return filepath.Join(dir, "stdout.log"), filepath.Join(dir, "stderr.log")
}

func WritePaths(stdout, stderr string, redirect bool) (string, string) {
	if redirect {
		return stdout, stdout
	}
	return stdout, stderr
}

func ValidateDirectory(dir, dataRoot string) error {
	if dir == "" {
		return nil
	}
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) || clean == "." {
		return errcode.E(errcode.INVALID, "log path: directory must be an absolute path")
	}
	if clean == "/" {
		return errcode.E(errcode.INVALID, "log path: directory must not be /")
	}
	for _, p := range systemLogDirPrefixes {
		if clean == p || strings.HasPrefix(clean, p+"/") {
			return errcode.E(errcode.INVALID, "log path: directory is not allowed under "+p)
		}
	}
	if dataRoot == "" {
		return nil
	}
	rel, err := filepath.Rel(filepath.Clean(dataRoot), clean)
	if err != nil {
		return nil
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return nil
	}
	first, _, _ := strings.Cut(rel, "/")
	for _, name := range agentInternalNames {
		if first == name {
			return errcode.E(errcode.INVALID, "log path: directory must not point at Agent internal data (store.db, raft, cluster, runtime, shim)")
		}
	}
	return nil
}

func deviceID(path string) (uint64, bool) {
	for p := path; p != "" && p != string(filepath.Separator); p = filepath.Dir(p) {
		var st unix.Stat_t
		if err := unix.Stat(p, &st); err == nil {
			return uint64(st.Dev), true
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return 0, false
}

func SameDevice(a, b string) bool {
	da, oka := deviceID(a)
	db, okb := deviceID(b)
	return oka && okb && da == db
}
