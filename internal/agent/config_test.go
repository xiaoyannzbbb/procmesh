package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/agent"
)

func TestRun_RejectsMissingExplicitConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := agent.Run(ctx, agent.Options{
		DataDir:    t.TempDir(),
		Listen:     "127.0.0.1:0",
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
	})
	if err == nil {
		t.Fatal("expected missing config error")
	}
}

func TestRun_AppliesAutoDeleteFromConfig(t *testing.T) {
	// 不必起完整 HTTP：若 Run 在 Listen 前就 Load，缺失文件测试已够。
	// 本测试写合法 yaml（auto_delete: true）并 Listen :0，OnListen 后 cancel。
	// 只断言 Run 返回 nil（ctx 取消），配置非法才会非 nil。
	dir := t.TempDir()
	cfg := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(cfg, []byte("disk:\n  auto_delete: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx, agent.Options{
			DataDir:    dir,
			Listen:     "127.0.0.1:0",
			ConfigPath: cfg,
			OnListen:   func(string) { cancel() },
		})
	}()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timeout")
	}
}
