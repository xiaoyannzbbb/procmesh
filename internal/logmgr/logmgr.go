package logmgr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/qleelulu/procmesh/internal/paths"
	"golang.org/x/sys/unix"
)

const (
	dirMode  = 0o750
	fileMode = 0o640

	warnPct      = 85
	cleanupPct   = 90
	emergencyPct = 95
	targetPct    = 85

	defaultTailLines = 1000
	maxTailLines     = 10000
	tailChunk        = 32 * 1024
)

// DiskUsage reports used disk percent for root.
type DiskUsage func(root string) (usedPercent float64, err error)

// Level is disk-pressure severity.
type Level int

const (
	OK Level = iota
	Warn
	Cleanup
	Emergency
)

// Manager enforces log file paths and disk protection on Root.
type Manager struct {
	Root  string
	Usage DiskUsage
	Now   func() time.Time

	mu      sync.Mutex
	blocked bool
}

// InstancePaths returns stdout/stderr files under logs/<processID>/<instanceID>/.
func InstancePaths(layout paths.Layout, processID, instanceID string) (stdout, stderr string) {
	dir := filepath.Join(layout.LogDir, processID, instanceID)
	return filepath.Join(dir, "stdout.log"), filepath.Join(dir, "stderr.log")
}

// Prepare creates parent directories and empty log files without truncating existing ones.
func Prepare(paths ...string) error {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), dirMode); err != nil {
			return fmt.Errorf("mkdir log parent: %w", err)
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, fileMode)
		if err != nil {
			return fmt.Errorf("create log: %w", err)
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Protect classifies disk usage and, at Cleanup/Emergency, deletes oldest logs
// under logs/ until used ≤ 85 or no log files remain.
func (m *Manager) Protect(ctx context.Context) (Level, error) {
	if m == nil {
		return OK, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	used, err := m.usedPercent()
	if err != nil {
		return 0, err
	}
	level := classify(used)
	if level >= Cleanup {
		used, err = m.deleteOldestLogs(ctx, targetPct)
		if err != nil {
			return level, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if level == Emergency {
		m.blocked = true
	}
	if m.blocked && used <= cleanupPct {
		m.blocked = false
	}
	return level, nil
}

// WritesAllowed is false after Emergency until used ≤ 90.
func (m *Manager) WritesAllowed() bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.blocked
}

// Tail returns the last maxLines of path. maxLines defaults to 1000 and caps at 10000.
func Tail(path string, maxLines int) ([]string, error) {
	if maxLines <= 0 {
		maxLines = defaultTailLines
	}
	if maxLines > maxTailLines {
		maxLines = maxTailLines
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size == 0 {
		return []string{}, nil
	}

	var (
		data     []byte
		off      = size
		newlines int
	)
	for off > 0 && newlines <= maxLines {
		n := int64(tailChunk)
		if n > off {
			n = off
		}
		off -= n
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
			return nil, err
		}
		data = append(buf, data...)
		newlines = bytes.Count(data, []byte{'\n'})
	}
	if off > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	if len(data) == 0 {
		return []string{}, nil
	}
	all := strings.Split(string(data), "\n")
	if len(all) > maxLines {
		all = all[len(all)-maxLines:]
	}
	return all, nil
}

func (m *Manager) usedPercent() (float64, error) {
	fn := m.Usage
	if fn == nil {
		fn = defaultUsage
	}
	return fn(m.Root)
}

func classify(used float64) Level {
	switch {
	case used > emergencyPct:
		return Emergency
	case used > cleanupPct:
		return Cleanup
	case used > warnPct:
		return Warn
	default:
		return OK
	}
}

type logFile struct {
	path    string
	modTime time.Time
}

func (m *Manager) deleteOldestLogs(ctx context.Context, stopAt float64) (float64, error) {
	used, err := m.usedPercent()
	if err != nil {
		return 0, err
	}
	for used > stopAt {
		if err := ctx.Err(); err != nil {
			return used, err
		}
		files, err := listDeletableLogs(m.Root)
		if err != nil {
			return used, err
		}
		if len(files) == 0 {
			return used, nil
		}
		if err := os.Remove(files[0].path); err != nil && !os.IsNotExist(err) {
			return used, fmt.Errorf("remove log: %w", err)
		}
		used, err = m.usedPercent()
		if err != nil {
			return used, err
		}
	}
	return used, nil
}

func listDeletableLogs(root string) ([]logFile, error) {
	dir := filepath.Join(root, "logs")
	var files []logFile
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".log.gz") {
			return nil
		}
		if protectedPath(root, path) {
			return nil
		}
		files = append(files, logFile{path: path, modTime: info.ModTime()})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].modTime.Equal(files[j].modTime) {
			return files[i].modTime.Before(files[j].modTime)
		}
		return files[i].path < files[j].path
	})
	return files, nil
}

func protectedPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	rel = filepath.ToSlash(rel)
	if rel == "store.db" || strings.HasPrefix(rel, "store.db/") {
		return true
	}
	for _, prefix := range []string{"raft", "cluster", "runtime"} {
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return false
}

func defaultUsage(root string) (float64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil {
		return 0, fmt.Errorf("statfs: %w", err)
	}
	if st.Blocks == 0 || st.Bfree >= st.Blocks {
		return 0, nil
	}
	used := st.Blocks - st.Bfree
	return 100 * float64(used) / float64(st.Blocks), nil
}
