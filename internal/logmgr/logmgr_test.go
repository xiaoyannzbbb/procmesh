package logmgr_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/paths"
)

func TestProtect_Over95PercentDisablesWritesRegardlessOfFreeSpace(t *testing.T) {
	root := t.TempDir()
	logPath := writeLog(t, root, "p", "i", "stdout.log", "keep", time.Now())
	pol := logmgr.DefaultPolicy()
	pol.AutoDelete = true
	m := &logmgr.Manager{
		Root:   root,
		Usage:  func(string) (float64, error) { return 97, nil },
		Now:    time.Now,
		Policy: pol,
	}
	lvl, err := m.Protect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lvl != logmgr.Emergency {
		t.Fatalf("lvl=%v want Emergency when used>95%% even if 13GiB free", lvl)
	}
	if m.WritesAllowed() {
		t.Fatal("must disable writes above 95% regardless of remaining bytes")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("emergency must still delete old logs")
	}
}

func TestProtect_EmergencyStopsWrites(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs", "p", "i")
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(logDir, "stdout.log")
	if err := os.WriteFile(old, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	pol := logmgr.DefaultPolicy()
	pol.AutoDelete = true
	m := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return 96, nil }, Now: time.Now, Policy: pol}
	lvl, err := m.Protect(context.Background())
	if err != nil || lvl != logmgr.Emergency || m.WritesAllowed() {
		t.Fatalf("lvl=%v allowed=%v err=%v", lvl, m.WritesAllowed(), err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expected old log removed")
	}
}

func TestProtect_WarnDoesNotDelete(t *testing.T) {
	root := t.TempDir()
	logPath := writeLog(t, root, "p", "i", "stdout.log", "keep", time.Now())
	m := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return 86, nil }, Now: time.Now}
	lvl, err := m.Protect(context.Background())
	if err != nil || lvl != logmgr.Warn {
		t.Fatalf("lvl=%v err=%v", lvl, err)
	}
	if !m.WritesAllowed() {
		t.Fatal("warn must allow writes")
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatal("warn must not delete logs")
	}
}

func TestProtect_OKBelowThreshold(t *testing.T) {
	root := t.TempDir()
	m := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return 85, nil }, Now: time.Now}
	lvl, err := m.Protect(context.Background())
	if err != nil || lvl != logmgr.OK || !m.WritesAllowed() {
		t.Fatalf("lvl=%v allowed=%v err=%v", lvl, m.WritesAllowed(), err)
	}
}

func TestProtect_CleanupDeletesOldestUntil85(t *testing.T) {
	root := t.TempDir()
	oldest := writeLog(t, root, "p", "i", "old.log", "a", time.Now().Add(-3*time.Hour))
	mid := writeLog(t, root, "p", "i", "mid.log", "b", time.Now().Add(-2*time.Hour))
	newest := writeLog(t, root, "p", "i", "new.log", "c", time.Now().Add(-time.Hour))
	gz := writeLog(t, root, "p", "i", "old.log.gz", "z", time.Now().Add(-4*time.Hour))

	pol := logmgr.DefaultPolicy()
	pol.AutoDelete = true
	m := &logmgr.Manager{
		Root: root,
		Usage: func(string) (float64, error) {
			switch countLogs(t, root) {
			case 4:
				return 91, nil
			case 3:
				return 88, nil
			default:
				return 84, nil
			}
		},
		Now:    time.Now,
		Policy: pol,
	}
	lvl, err := m.Protect(context.Background())
	if err != nil || lvl != logmgr.Cleanup {
		t.Fatalf("lvl=%v err=%v", lvl, err)
	}
	if !m.WritesAllowed() {
		t.Fatal("cleanup without emergency must allow writes")
	}
	if _, err := os.Stat(gz); !os.IsNotExist(err) {
		t.Fatal("expected oldest gz removed")
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatal("expected oldest log removed")
	}
	if _, err := os.Stat(mid); err != nil {
		t.Fatal("mid should remain once used<=85")
	}
	if _, err := os.Stat(newest); err != nil {
		t.Fatal("newest should remain")
	}
	if n := countLogs(t, root); n != 2 {
		t.Fatalf("remaining=%d", n)
	}
}

func TestProtect_NeverDeletesProtectedPaths(t *testing.T) {
	root := t.TempDir()
	protected := []string{
		filepath.Join(root, "store.db"),
		filepath.Join(root, "raft", "meta.log"),
		filepath.Join(root, "cluster", "ca.log"),
		filepath.Join(root, "runtime", "p1_0.log"),
	}
	for _, p := range protected {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("keep"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	logPath := writeLog(t, root, "p", "i", "stdout.log", "x", time.Now())
	pol := logmgr.DefaultPolicy()
	pol.AutoDelete = true
	m := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return 96, nil }, Now: time.Now, Policy: pol}
	if _, err := m.Protect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("log under logs/ should be removed")
	}
	for _, p := range protected {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("protected path removed: %s", p)
		}
	}
}

func TestProtect_WritesAllowedRecoversAt90(t *testing.T) {
	root := t.TempDir()
	used := 96.0
	m := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return used, nil }, Now: time.Now}
	if _, err := m.Protect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.WritesAllowed() {
		t.Fatal("expected blocked after emergency")
	}
	used = 91
	if _, err := m.Protect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.WritesAllowed() {
		t.Fatal("still blocked while used>90")
	}
	used = 90
	lvl, err := m.Protect(context.Background())
	if err != nil || lvl != logmgr.Warn {
		t.Fatalf("lvl=%v err=%v", lvl, err)
	}
	if !m.WritesAllowed() {
		t.Fatal("writes allowed when used<=90")
	}
}

func TestInstancePaths(t *testing.T) {
	layout := paths.New("/data")
	stdout, stderr := logmgr.InstancePaths(layout, "p1", "p1:0")
	if stdout != "/data/logs/p1/p1:0/stdout.log" {
		t.Fatalf("stdout=%q", stdout)
	}
	if stderr != "/data/logs/p1/p1:0/stderr.log" {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestPrepare_CreatesParentsAndEmptyFiles(t *testing.T) {
	root := t.TempDir()
	stdout := filepath.Join(root, "logs", "p1", "p1:0", "stdout.log")
	stderr := filepath.Join(root, "logs", "p1", "p1:0", "stderr.log")
	if err := logmgr.Prepare(stdout, stderr); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{stdout, stderr} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() != 0 {
			t.Fatalf("%s size=%d", p, st.Size())
		}
	}
	if err := os.WriteFile(stdout, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := logmgr.Prepare(stdout); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(stdout)
	if err != nil || string(b) != "keep" {
		t.Fatalf("prepare truncated existing: %q %v", b, err)
	}
}

func TestTail_LastNLinesDefaultAndCap(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "out.log")
	var b strings.Builder
	for i := 1; i <= 12; i++ {
		b.WriteString(strings.Repeat("x", i))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := logmgr.Tail(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != strings.Repeat("x", 10) || got[2] != strings.Repeat("x", 12) {
		t.Fatalf("%q", got)
	}

	many := filepath.Join(root, "many.log")
	var big strings.Builder
	for i := 0; i < 1200; i++ {
		big.WriteString("L")
		big.WriteString(strings.Repeat("0", 3))
		big.WriteByte('\n')
	}
	if err := os.WriteFile(many, []byte(big.String()), 0o640); err != nil {
		t.Fatal(err)
	}
	def, err := logmgr.Tail(many, 0)
	if err != nil || len(def) != 1000 {
		t.Fatalf("default max want 1000 got %d err=%v", len(def), err)
	}
	capped, err := logmgr.Tail(many, 50000)
	if err != nil || len(capped) != 1200 {
		t.Fatalf("cap should not invent lines, got %d err=%v", len(capped), err)
	}
}

func TestTail_HardCap10000(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "huge.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12000; i++ {
		if _, err := f.WriteString("line\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := logmgr.Tail(path, 20000)
	if err != nil || len(got) != 10000 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
}

func TestDefaultUsage_Statfs(t *testing.T) {
	root := t.TempDir()
	m := &logmgr.Manager{Root: root, Now: time.Now}
	lvl, err := m.Protect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lvl < logmgr.OK || lvl > logmgr.Emergency {
		t.Fatalf("unexpected level %v", lvl)
	}
}

func TestRotate_BySizeAndMaxFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stdout.log")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 64), 0o640); err != nil {
		t.Fatal(err)
	}
	pol := logmgr.RotatePolicy{MaxSize: 32, MaxFiles: 2, Compress: false}
	if err := logmgr.Rotate(p, pol, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Size() != 0 {
		t.Fatalf("current log should be truncated, size=%d", st.Size())
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, bytes.Repeat([]byte("y"), 64), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := logmgr.Rotate(p, pol, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".2"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, bytes.Repeat([]byte("z"), 64), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := logmgr.Rotate(p, pol, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".3"); !os.IsNotExist(err) {
		t.Fatal("MaxFiles=2 must drop path.3")
	}
	b1, err := os.ReadFile(p + ".1")
	if err != nil || string(b1) != strings.Repeat("z", 64) {
		t.Fatalf("path.1 want newest archive: %q %v", b1, err)
	}
	b2, err := os.ReadFile(p + ".2")
	if err != nil || string(b2) != strings.Repeat("y", 64) {
		t.Fatalf("path.2 want previous archive: %q %v", b2, err)
	}
}

func TestRotate_MaxAgeDeletes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stdout.log")
	if err := os.WriteFile(p, []byte("cur"), 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1_000_000, 0)
	arch := p + ".1"
	if err := os.WriteFile(arch, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(arch, old, old); err != nil {
		t.Fatal(err)
	}
	pol := logmgr.RotatePolicy{MaxSize: 32, MaxFiles: 2, MaxAge: time.Second, Compress: false}
	if err := logmgr.Rotate(p, pol, old.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(arch); !os.IsNotExist(err) {
		t.Fatal("expected aged archive removed")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("current log must remain")
	}
}

func TestRotate_Compress(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stdout.log")
	pol := logmgr.RotatePolicy{MaxSize: 32, MaxFiles: 2, Compress: true}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 64), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := logmgr.Rotate(p, pol, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".1.gz"); !os.IsNotExist(err) {
		t.Fatal("delaycompress: path.1 must stay uncompressed")
	}

	if err := os.WriteFile(p, bytes.Repeat([]byte("y"), 64), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := logmgr.Rotate(p, pol, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".2.gz"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".2"); !os.IsNotExist(err) {
		t.Fatal("closed path.2 should be gzipped")
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("path.1 must stay uncompressed")
	}
	if _, err := os.Stat(p + ".1.gz"); !os.IsNotExist(err) {
		t.Fatal("path.1 must not be gzipped this round")
	}
}

func TestProtect_DeletesRotatedArchives(t *testing.T) {
	root := t.TempDir()
	gz := writeLog(t, root, "p", "i", "stdout.log.2.gz", "z", time.Now().Add(-3*time.Hour))
	arch := writeLog(t, root, "p", "i", "stdout.log.1", "a", time.Now().Add(-2*time.Hour))
	cur := writeLog(t, root, "p", "i", "stdout.log", "c", time.Now())

	pol := logmgr.DefaultPolicy()
	pol.AutoDelete = true
	m := &logmgr.Manager{
		Root: root,
		Usage: func(string) (float64, error) {
			switch countLogs(t, root) {
			case 3:
				return 96, nil
			case 2:
				return 91, nil
			default:
				return 84, nil
			}
		},
		Now:    time.Now,
		Policy: pol,
	}
	lvl, err := m.Protect(context.Background())
	if err != nil || lvl != logmgr.Emergency {
		t.Fatalf("lvl=%v err=%v", lvl, err)
	}
	if _, err := os.Stat(gz); !os.IsNotExist(err) {
		t.Fatal("expected stdout.log.2.gz removed")
	}
	if _, err := os.Stat(arch); !os.IsNotExist(err) {
		t.Fatal("expected stdout.log.1 removed")
	}
	if _, err := os.Stat(cur); err != nil {
		t.Fatal("current log should remain once used<=85")
	}
}

func TestRotate_NeverTouchesProtectedPaths(t *testing.T) {
	root := t.TempDir()
	protected := []string{
		filepath.Join(root, "store.db"),
		filepath.Join(root, "raft", "meta.log"),
		filepath.Join(root, "cluster", "ca.log"),
		filepath.Join(root, "runtime", "p1_0.log"),
	}
	pol := logmgr.RotatePolicy{MaxSize: 1, MaxFiles: 2, Compress: false}
	body := bytes.Repeat([]byte("x"), 64)
	for _, p := range protected {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := logmgr.Rotate(p, pol, time.Now()); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("protected path mutated: %s", p)
		}
		if _, err := os.Stat(p + ".1"); !os.IsNotExist(err) {
			t.Fatalf("protected path rotated: %s", p)
		}
	}
	// processID "runtime" lives under logs/, not data-root runtime/.
	logPath := filepath.Join(root, "logs", "runtime", "i", "stdout.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, body, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := logmgr.Rotate(logPath, pol, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(logPath); err != nil || st.Size() != 0 {
		t.Fatalf("logs/runtime must still rotate, size err=%v", err)
	}
}

func isLogOrArchive(name string) bool {
	if strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".log.gz") {
		return true
	}
	trimmed := strings.TrimSuffix(name, ".gz")
	i := strings.LastIndex(trimmed, ".")
	if i < 0 || !strings.HasSuffix(trimmed[:i], ".log") {
		return false
	}
	rest := trimmed[i+1:]
	if rest == "" {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func writeLog(t *testing.T, root, processID, instanceID, name, body string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, "logs", processID, instanceID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func countLogs(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.Walk(filepath.Join(root, "logs"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if isLogOrArchive(info.Name()) {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDefaultPolicy_DoesNotAutoDelete(t *testing.T) {
	p := logmgr.DefaultPolicy()
	if p.WarnPercent != 85 || p.CleanupPercent != 90 || p.EmergencyPercent != 95 {
		t.Fatalf("%+v", p)
	}
	if p.AutoDelete || !p.EmergencyStopWrites {
		t.Fatalf("%+v", p)
	}
}

func TestProtect_AutoDeleteFalseKeepsLogsAt91(t *testing.T) {
	root := t.TempDir()
	logPath := writeLog(t, root, "p", "i", "stdout.log", "keep", time.Now())
	m := &logmgr.Manager{
		Root:   root,
		Usage:  func(string) (float64, error) { return 91, nil },
		Policy: logmgr.DefaultPolicy(), // AutoDelete=false
		Now:    time.Now,
	}
	lvl, err := m.Protect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lvl != logmgr.Cleanup {
		t.Fatalf("lvl=%v", lvl)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatal("auto_delete false must keep logs")
	}
}

func TestProtect_EmergencyStopWritesFalseAllowsWritesAt96(t *testing.T) {
	root := t.TempDir()
	p := logmgr.DefaultPolicy()
	p.EmergencyStopWrites = false
	m := &logmgr.Manager{
		Root:   root,
		Usage:  func(string) (float64, error) { return 96, nil },
		Policy: p,
		Now:    time.Now,
	}
	lvl, err := m.Protect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lvl != logmgr.Emergency {
		t.Fatalf("lvl=%v", lvl)
	}
	if !m.WritesAllowed() {
		t.Fatal("emergency_stop_writes false must allow writes")
	}
}

func TestPolicy_ValidateOrder(t *testing.T) {
	p := logmgr.DefaultPolicy()
	p.WarnPercent = 90
	p.CleanupPercent = 85
	if err := p.Validate(); err == nil {
		t.Fatal("expected invalid order")
	}
}
