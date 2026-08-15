package api

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

func TestLog_TailDefaultLinesAndCap(t *testing.T) {
	ctx := context.Background()
	proc, logs, _, layout := newLogClients(t)
	processID, instID := seedProcess(t, proc, "web", 1)
	writeInstanceLog(t, layout, processID, instID, "stdout", numberedLines(150))

	got, err := logs.TailLogs(ctx, connect.NewRequest(&procmeshv1.TailLogsRequest{IdOrName: "web"}))
	if err != nil {
		t.Fatal(err)
	}
	lines := got.Msg.GetLines()
	if len(lines) != DefaultTailLines {
		t.Fatalf("default lines=%d want %d", len(lines), DefaultTailLines)
	}
	if lines[0] != "L00050" || lines[len(lines)-1] != "L00149" {
		t.Fatalf("default range %q..%q", lines[0], lines[len(lines)-1])
	}

	writeInstanceLog(t, layout, processID, instID, "stdout", numberedLines(12000))
	capped, err := logs.TailLogs(ctx, connect.NewRequest(&procmeshv1.TailLogsRequest{
		IdOrName: "web",
		Lines:    20000,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(capped.Msg.GetLines()); n != MaxTailLines {
		t.Fatalf("cap lines=%d want %d", n, MaxTailLines)
	}
}

func TestLog_TailMissingProcess(t *testing.T) {
	ctx := context.Background()
	_, logs, _, _ := newLogClients(t)
	_, err := logs.TailLogs(ctx, connect.NewRequest(&procmeshv1.TailLogsRequest{IdOrName: "missing"}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestLog_TailMissingFileEmpty(t *testing.T) {
	ctx := context.Background()
	proc, logs, _, _ := newLogClients(t)
	seedProcess(t, proc, "web", 1)
	got, err := logs.TailLogs(ctx, connect.NewRequest(&procmeshv1.TailLogsRequest{IdOrName: "web"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.GetLines()) != 0 {
		t.Fatalf("lines=%q", got.Msg.GetLines())
	}
}

func TestLog_TailMergesInstances(t *testing.T) {
	ctx := context.Background()
	proc, logs, _, layout := newLogClients(t)
	processID, _ := seedProcess(t, proc, "web", 2)
	insts := listInstanceIDs(t, proc, "web")
	if len(insts) != 2 {
		t.Fatalf("insts=%v", insts)
	}
	writeInstanceLog(t, layout, processID, insts[0], "stdout", "one\n")
	writeInstanceLog(t, layout, processID, insts[1], "stdout", "two\n")
	got, err := logs.TailLogs(ctx, connect.NewRequest(&procmeshv1.TailLogsRequest{IdOrName: "web"}))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got.Msg.GetLines(), ",")
	if !strings.Contains(joined, "one") || !strings.Contains(joined, "two") {
		t.Fatalf("merged %q", joined)
	}
}

func TestLog_DownloadTruncates(t *testing.T) {
	ctx := context.Background()
	proc, logs, _, layout := newLogClients(t)
	processID, instID := seedProcess(t, proc, "web", 1)
	body := bytes.Repeat([]byte("x"), MaxDownloadSize+1024)
	writeInstanceLog(t, layout, processID, instID, "stdout", string(body))

	stream, err := logs.DownloadLogs(ctx, connect.NewRequest(&procmeshv1.DownloadLogsRequest{
		IdOrName:   "web",
		InstanceId: instID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var (
		total int
		eof   bool
		nmsg  int
	)
	for stream.Receive() {
		nmsg++
		msg := stream.Msg()
		total += len(msg.GetData())
		eof = msg.GetEof()
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if nmsg == 0 || !eof {
		t.Fatalf("msgs=%d eof=%v", nmsg, eof)
	}
	if total != MaxDownloadSize {
		t.Fatalf("downloaded %d want %d", total, MaxDownloadSize)
	}
}

func TestLog_StreamCancel(t *testing.T) {
	proc, logs, _, layout := newLogClients(t)
	processID, instID := seedProcess(t, proc, "web", 1)
	writeInstanceLog(t, layout, processID, instID, "stdout", "ready\n")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		stream, err := logs.StreamLogs(ctx, connect.NewRequest(&procmeshv1.StreamLogsRequest{
			IdOrName:   "web",
			InstanceId: instID,
		}))
		if err != nil {
			done <- err
			return
		}
		for stream.Receive() {
		}
		done <- stream.Err()
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not end after cancel")
	}
}

func TestLog_StreamRequiresInstanceWhenMany(t *testing.T) {
	ctx := context.Background()
	proc, logs, _, _ := newLogClients(t)
	seedProcess(t, proc, "web", 2)
	stream, err := logs.StreamLogs(ctx, connect.NewRequest(&procmeshv1.StreamLogsRequest{IdOrName: "web"}))
	if err != nil {
		code, detail := connectDetail(t, err)
		if code != connect.CodeInvalidArgument || detail != "INVALID" {
			t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
		}
		return
	}
	if stream.Receive() {
		t.Fatalf("unexpected chunk %+v", stream.Msg())
	}
	code, detail := connectDetail(t, stream.Err())
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, stream.Err())
	}
	if !strings.Contains(stream.Err().Error(), "instance_id required") {
		t.Fatalf("err=%v", stream.Err())
	}
}

func TestLog_StreamTooManyUnavailable(t *testing.T) {
	proc, logs, _, layout, api := newLogClientsAPI(t)
	processID, instID := seedProcess(t, proc, "web", 1)
	writeInstanceLog(t, layout, processID, instID, "stdout", "ready\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < MaxConcurrentStreams; i++ {
		go func() {
			st, err := logs.StreamLogs(ctx, connect.NewRequest(&procmeshv1.StreamLogsRequest{
				IdOrName:   "web",
				InstanceId: instID,
			}))
			if err != nil {
				return
			}
			for st.Receive() {
			}
		}()
	}
	waitHeldStreams(t, api, MaxConcurrentStreams)

	extraCtx, extraCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer extraCancel()
	extra, err := logs.StreamLogs(extraCtx, connect.NewRequest(&procmeshv1.StreamLogsRequest{
		IdOrName:   "web",
		InstanceId: instID,
	}))
	if err != nil {
		code, detail := connectDetail(t, err)
		if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
			t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
		}
		return
	}
	if extra.Receive() {
		t.Fatalf("unexpected chunk %+v", extra.Msg())
	}
	code, detail := connectDetail(t, extra.Err())
	if code != connect.CodeUnavailable || detail != "UNAVAILABLE" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, extra.Err())
	}
}

func TestLog_DownloadRequiresInstanceWhenMany(t *testing.T) {
	ctx := context.Background()
	proc, logs, _, _ := newLogClients(t)
	seedProcess(t, proc, "web", 2)
	stream, err := logs.DownloadLogs(ctx, connect.NewRequest(&procmeshv1.DownloadLogsRequest{IdOrName: "web"}))
	if err != nil {
		code, detail := connectDetail(t, err)
		if code != connect.CodeInvalidArgument || detail != "INVALID" {
			t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
		}
		return
	}
	if stream.Receive() {
		t.Fatalf("unexpected chunk %+v", stream.Msg())
	}
	code, detail := connectDetail(t, stream.Err())
	if code != connect.CodeInvalidArgument || detail != "INVALID" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, stream.Err())
	}
	if !strings.Contains(stream.Err().Error(), "instance_id required") {
		t.Fatalf("err=%v", stream.Err())
	}
}

func TestLog_TailUnknownInstance(t *testing.T) {
	ctx := context.Background()
	proc, logs, _, _ := newLogClients(t)
	seedProcess(t, proc, "web", 1)
	_, err := logs.TailLogs(ctx, connect.NewRequest(&procmeshv1.TailLogsRequest{
		IdOrName:   "web",
		InstanceId: "other:0",
	}))
	code, detail := connectDetail(t, err)
	if code != connect.CodeNotFound || detail != "NOT_FOUND" {
		t.Fatalf("code=%v detail=%s err=%v", code, detail, err)
	}
}

func TestLog_TailForwardsToOwner(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	fakeCli := &fakeLogClient{
		tailOut: connect.NewResponse(&procmeshv1.TailLogsResponse{Lines: []string{"remote-line"}}),
	}
	fwd := &fakeForwarder{logs: fakeCli}
	c := serveLogAPI(t, &LogAPI{
		Mgr:     m,
		LocalID: "aaa",
		Router:  remoteOwnerRouter("aaa", "ccc", "nginx"),
		Forward: fwd,
	})

	got, err := c.TailLogs(ctx, connect.NewRequest(&procmeshv1.TailLogsRequest{IdOrName: "nginx", Lines: 20}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.GetLines()) != 1 || got.Msg.GetLines()[0] != "remote-line" {
		t.Fatalf("lines=%q", got.Msg.GetLines())
	}
	if fwd.logCalls() != 1 {
		t.Fatalf("forward Log calls=%d", fwd.logCalls())
	}
	tails := fakeCli.tailReqs()
	if len(tails) != 1 {
		t.Fatalf("TailLogs calls=%d", len(tails))
	}
	if tails[0].Msg.GetIdOrName() != "nginx" || tails[0].Msg.GetLines() != 20 {
		t.Fatalf("req %+v", tails[0].Msg)
	}
	if rpc.SourceOf(tails[0].Header()) != "aaa" || rpc.TargetOf(tails[0].Header()) != "ccc" {
		t.Fatalf("source=%q target=%q", rpc.SourceOf(tails[0].Header()), rpc.TargetOf(tails[0].Header()))
	}
	specs, err := m.ListSpecs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 0 {
		t.Fatalf("local specs %+v", specs)
	}
}

func seedProcess(t *testing.T, proc procmeshv1connect.ProcessServiceClient, name string, instances int32) (processID, instanceID string) {
	t.Helper()
	ctx := context.Background()
	applied, err := proc.ApplyProcess(ctx, connect.NewRequest(&procmeshv1.ApplyProcessRequest{
		Meta: &procmeshv1.MutationMeta{OperationId: "op-" + name, Operator: "t"},
		Spec: &procmeshv1.ProcessSpec{Name: name, Command: "/bin/true", Instances: instances},
	}))
	if err != nil {
		t.Fatal(err)
	}
	processID = applied.Msg.GetSpec().GetProcessId()
	ids := listInstanceIDs(t, proc, name)
	if len(ids) == 0 {
		t.Fatal("no instances")
	}
	return processID, ids[0]
}

func listInstanceIDs(t *testing.T, proc procmeshv1connect.ProcessServiceClient, name string) []string {
	t.Helper()
	got, err := proc.GetProcess(context.Background(), connect.NewRequest(&procmeshv1.GetProcessRequest{IdOrName: name}))
	if err != nil {
		t.Fatal(err)
	}
	insts := got.Msg.GetProcess().GetInstances()
	ids := make([]string, 0, len(insts))
	for _, inst := range insts {
		ids = append(ids, inst.GetInstanceId())
	}
	return ids
}

func writeInstanceLog(t *testing.T, layout paths.Layout, processID, instanceID, stream, body string) {
	t.Helper()
	stdout, stderr := logmgr.InstancePaths(layout, processID, instanceID)
	path := stdout
	if stream == "stderr" {
		path = stderr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
}

func waitHeldStreams(t *testing.T, api *LogAPI, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if api.heldStreams() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("held %d want %d", api.heldStreams(), n)
}

func numberedLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "L%05d\n", i)
	}
	return b.String()
}
