package logmgr

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFollow_ReadsAppendedBytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stdout.log")
	if err := os.WriteFile(p, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, errCh := Follow(ctx, p, false)
	got := readOne(t, ch, errCh)
	if !bytes.Contains(got, []byte("a\n")) {
		t.Fatalf("%q", got)
	}
	if err := os.WriteFile(p, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = readOne(t, ch, errCh)
	if !bytes.Contains(got, []byte("b\n")) {
		t.Fatalf("%q", got)
	}
}

func TestFollow_FromEndSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stdout.log")
	if err := os.WriteFile(p, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, errCh := Follow(ctx, p, true)
	// Let Follow open and seek to the current end before appending.
	time.Sleep(100 * time.Millisecond)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("new\n")); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got := readOne(t, ch, errCh)
	if !bytes.Contains(got, []byte("new\n")) {
		t.Fatalf("%q", got)
	}
	if bytes.Equal(got, []byte("old\n")) {
		t.Fatalf("fromEnd sent existing: %q", got)
	}
}

func TestFollow_CancelClosesChannels(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stdout.log")
	if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, errCh := Follow(ctx, p, true)
	cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for ch != nil || errCh != nil {
		select {
		case _, ok := <-ch:
			if !ok {
				ch = nil
			}
		case _, ok := <-errCh:
			if !ok {
				errCh = nil
			}
		case <-timer.C:
			t.Fatal("channels not closed after cancel")
		}
	}
}

func readOne(t *testing.T, ch <-chan []byte, errCh <-chan error) []byte {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case b, ok := <-ch:
		if !ok {
			t.Fatal("data channel closed")
		}
		return b
	case err := <-errCh:
		t.Fatalf("follow err: %v", err)
		return nil
	case <-timer.C:
		t.Fatal("timeout waiting for follow chunk")
		return nil
	}
}
