package logmgr_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/paths"
)

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
	m := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return 96, nil }, Now: time.Now}
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
		Now: time.Now,
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
	m := &logmgr.Manager{Root: root, Usage: func(string) (float64, error) { return 96, nil }, Now: time.Now}
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
		if strings.HasSuffix(info.Name(), ".log") || strings.HasSuffix(info.Name(), ".log.gz") {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
