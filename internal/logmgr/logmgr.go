package logmgr

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// RotatePolicy is size/age/file/compress limits for a single log path.
// Fields mirror process.LogPolicy without importing process (cycle).
type RotatePolicy struct {
	MaxSize  int64
	MaxFiles int
	MaxAge   time.Duration
	Compress bool
}

// Rotate enforces pol on path: size-shift, delaycompress path.2+, cap files, drop aged.
// Never touches store.db, raft/, cluster/, or runtime/.
func Rotate(path string, pol RotatePolicy, now time.Time) error {
	if path == "" || rotateProtected(path) {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := rotateBySize(path, pol); err != nil {
		return err
	}
	if pol.Compress {
		if err := compressArchives(path); err != nil {
			return err
		}
	}
	if err := pruneExcessArchives(path, pol.MaxFiles); err != nil {
		return err
	}
	return pruneAgedArchives(path, pol.MaxAge, now)
}

func rotateAll(paths []string, pol RotatePolicy, now time.Time) error {
	for _, p := range paths {
		if err := Rotate(p, pol, now); err != nil {
			return err
		}
	}
	return nil
}

func rotateBySize(path string, pol RotatePolicy) error {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if pol.MaxSize <= 0 || st.Size() <= pol.MaxSize {
		return nil
	}
	maxFiles := pol.MaxFiles
	if maxFiles < 0 {
		maxFiles = 0
	}
	for i := maxFiles; i >= 1; i-- {
		for _, suf := range []string{"", ".gz"} {
			src := fmt.Sprintf("%s.%d%s", path, i, suf)
			if _, err := os.Stat(src); err != nil {
				continue
			}
			if i >= maxFiles {
				if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("rotate remove: %w", err)
				}
				continue
			}
			dst := fmt.Sprintf("%s.%d%s", path, i+1, suf)
			if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("rotate shift: %w", err)
			}
		}
	}
	if maxFiles < 1 {
		return recreateEmpty(path)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("rotate rename: %w", err)
	}
	return recreateEmpty(path)
}

func recreateEmpty(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileMode)
	if err != nil {
		return fmt.Errorf("recreate log: %w", err)
	}
	if err := f.Chmod(fileMode); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func compressArchives(path string) error {
	archs, err := listArchives(path)
	if err != nil {
		return err
	}
	for _, a := range archs {
		// delaycompress: child may still hold the just-renamed path.1 inode.
		if a.gz || a.index < 2 {
			continue
		}
		if err := gzipFile(a.path); err != nil {
			return err
		}
	}
	return nil
}

func gzipFile(src string) error {
	in, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer in.Close()

	dst := src + ".gz"
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileMode)
	if err != nil {
		return err
	}
	gw := gzip.NewWriter(out)
	if _, err := io.Copy(gw, in); err != nil {
		_ = gw.Close()
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := gw.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Chmod(fileMode); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func pruneExcessArchives(path string, maxFiles int) error {
	if maxFiles < 0 {
		maxFiles = 0
	}
	archs, err := listArchives(path)
	if err != nil {
		return err
	}
	for _, a := range archs {
		if a.index <= maxFiles {
			continue
		}
		if err := os.Remove(a.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("prune archive: %w", err)
		}
	}
	return nil
}

func pruneAgedArchives(path string, maxAge time.Duration, now time.Time) error {
	if maxAge <= 0 {
		return nil
	}
	archs, err := listArchives(path)
	if err != nil {
		return err
	}
	for _, a := range archs {
		st, err := os.Stat(a.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if now.Sub(st.ModTime()) <= maxAge {
			continue
		}
		if err := os.Remove(a.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("age archive: %w", err)
		}
	}
	return nil
}

type archive struct {
	path  string
	index int
	gz    bool
}

func listArchives(path string) ([]archive, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	prefix := base + "."
	var out []archive
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		gz := false
		if strings.HasSuffix(rest, ".gz") {
			gz = true
			rest = strings.TrimSuffix(rest, ".gz")
		}
		n, err := strconv.Atoi(rest)
		if err != nil || n < 1 {
			continue
		}
		out = append(out, archive{path: filepath.Join(dir, name), index: n, gz: gz})
	}
	return out, nil
}

func rotateProtected(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	// Process logs live under logs/<processID>/... and may reuse those names.
	if strings.Contains(clean, "/logs/") || strings.HasPrefix(clean, "logs/") {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		switch part {
		case "store.db", "raft", "cluster", "runtime":
			return true
		}
	}
	return false
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
		if !deletableLogName(info.Name()) {
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

func deletableLogName(name string) bool {
	if strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".log.gz") {
		return true
	}
	trimmed := strings.TrimSuffix(name, ".gz")
	i := strings.LastIndex(trimmed, ".")
	if i < 0 || !strings.HasSuffix(trimmed[:i], ".log") {
		return false
	}
	n, err := strconv.Atoi(trimmed[i+1:])
	return err == nil && n >= 1
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
