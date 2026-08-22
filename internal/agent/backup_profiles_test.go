package agent

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/qleelulu/procmesh/internal/agentcfg"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestNewBackupEngineResolvesNamedS3Profiles(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetClusterID(context.Background(), "cluster-1"); err != nil {
		t.Fatal(err)
	}

	engine := newBackupEngine(Options{
		DataDir: root,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Backup: agentcfg.Backup{S3Profiles: map[string]agentcfg.S3{
			"archive": {Endpoint: "http://127.0.0.1:18680", Bucket: "archive", Insecure: true},
		}},
	}, nil, st, nil, &rpcRuntime{nodeID: "node-1"}, nil)
	if engine == nil || engine.ResolveDestination == nil {
		t.Fatal("named destination resolver is not wired")
	}
	sink, err := engine.ResolveDestination("archive")
	if err != nil || sink == nil || sink.Name() != "s3" {
		t.Fatalf("archive sink = %v, %v", sink, err)
	}
	if _, err := engine.ResolveDestination("missing"); err == nil {
		t.Fatal("missing profile must fail")
	}
}
