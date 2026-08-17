package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/alert"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/store"
)

func TestQ4_ProcessKillWritesInbox(t *testing.T) {
	addr, _ := startClusterAgent(t, "")
	initAndLogin(t, addr)

	spec := writeShortLivedSpec(t)
	mustCLI(t, addr, "process", "apply", "--file", spec, "--expected-revision", "0")
	mustCLI(t, addr, "process", "start", "boom")
	waitObservedLong(t, addr, "boom", "EXITED", "FATAL", "BACKOFF")

	deadline := time.Now().Add(45 * time.Second)
	var out string
	for time.Now().Before(deadline) {
		out = mustCLI(t, addr, "alert", "list")
		up := strings.ToUpper(out)
		if strings.Contains(up, "PROCESS_EXIT") || strings.Contains(up, "PROCESS_FATAL") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("want PROCESS_EXIT or PROCESS_FATAL after short-lived process, got %s", out)
}

func TestQ4_LostQuorumRejectsPutChannelOwnerStillWritesInbox(t *testing.T) {
	addrA, _, cancelA := startClusterAgentCtl(t, "")
	addrC, rootC := startClusterAgent(t, "")
	joinTwo(t, addrA, addrC)
	cancelA()

	deadline := time.Now().Add(20 * time.Second)
	var code int
	var out, errb string
	for time.Now().Before(deadline) {
		code, out, errb = runCLIExit(t, addrC, "alert", "channel", "put",
			"--type", "WEBHOOK", "--name", "lost-q", "--config", `{"url":"http://127.0.0.1/hook"}`)
		if code != 0 && strings.Contains(strings.ToUpper(errb+out), "UNAVAILABLE") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if code == 0 {
		t.Fatalf("put channel after leader down must fail, stdout=%q", out)
	}
	if !strings.Contains(strings.ToUpper(errb+out), "UNAVAILABLE") {
		t.Fatalf("want UNAVAILABLE, got stdout=%q stderr=%q", out, errb)
	}

	listOut := mustCLI(t, addrC, "alert", "list")
	_ = listOut

	st := openAgentStore(t, rootC)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	if err := st.UpsertAlert(context.Background(), store.AlertRecord{
		AlertID: "q4-owner-inbox", Fingerprint: "PROCESS_EXIT:owner-local",
		Type: "PROCESS_EXIT", Severity: "WARNING", NodeID: readNodeID(t, rootC),
		ProcessID: "owner-local", PayloadJSON: `{}`, State: "FIRING",
		FirstAt: now, LastAt: now,
	}); err != nil {
		t.Fatalf("owner upsert inbox: %v", err)
	}
	got := mustCLI(t, addrC, "alert", "list")
	if !strings.Contains(got, "PROCESS_EXIT:owner-local") {
		t.Fatalf("owner inbox not listed after lost quorum: %s", got)
	}
}

func TestQ4_LeaderFailedNotSentByEveryFollower(t *testing.T) {
	addrA, rootA, cancelA := startClusterAgentCtl(t, "")
	addrC, rootC := startClusterAgent(t, "")
	idA := readNodeID(t, rootA)
	joinTwo(t, addrA, addrC)
	cancelA()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if alertHasFingerprint(t, rootC, "NODE_FAILED:"+idA) {
			t.Fatalf("follower wrote NODE_FAILED:%s", idA)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if alertHasFingerprint(t, rootC, "NODE_FAILED:"+idA) {
		t.Fatalf("non-leader must not write NODE_FAILED:%s", idA)
	}
}

func TestQ4_FiveChannelsFakeServer(t *testing.T) {
	addr, root := startClusterAgent(t, "")
	initAndLogin(t, addr)
	id := readNodeID(t, root)

	var hookN, wecomN, dingN, slackN atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hookN.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)
	wecom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wecomN.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(wecom.Close)
	ding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dingN.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ding.Close)
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackN.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slack.Close)
	smtpHost, smtpPort, smtpN := startFakeSMTP(t)

	mustCLI(t, addr, "alert", "channel", "put", "--type", "WEBHOOK", "--name", "hook1",
		"--config", `{"url":"`+hook.URL+`"}`)
	mustCLI(t, addr, "alert", "channel", "put", "--type", "WECOM", "--name", "wecom1",
		"--config", `{"webhook_url":"`+wecom.URL+`"}`)
	mustCLI(t, addr, "alert", "channel", "put", "--type", "DINGTALK", "--name", "ding1",
		"--config", `{"webhook_url":"`+ding.URL+`","secret":"SECtest"}`)
	mustCLI(t, addr, "alert", "channel", "put", "--type", "SLACK", "--name", "slack1",
		"--config", `{"webhook_url":"`+slack.URL+`"}`)
	mustCLI(t, addr, "alert", "channel", "put", "--type", "EMAIL", "--name", "mail1",
		"--config", fmt.Sprintf(`{"smtp_host":"%s","smtp_port":%d,"from":"a@b","to":["c@d"],"starttls":false}`, smtpHost, smtpPort))
	mustCLI(t, addr, "alert", "channel", "put", "--type", "WEB", "--name", "web1", "--config", `{}`)

	st := openAgentStore(t, root)
	defer func() { _ = st.Close() }()
	eng := &alert.Engine{
		Store:  st,
		NodeID: id,
		NewID:  func() string { return "q4-five-ch" },
		Policy: func() control.AlertPolicy { return control.DefaultAlertPolicy() },
		Channels: func() []control.AlertChannel {
			return []control.AlertChannel{
				{ChannelID: "hook1", Type: "WEBHOOK", Name: "hook1", Enabled: true, ConfigJSON: `{"url":"` + hook.URL + `"}`},
				{ChannelID: "wecom1", Type: "WECOM", Name: "wecom1", Enabled: true, ConfigJSON: `{"webhook_url":"` + wecom.URL + `"}`},
				{ChannelID: "ding1", Type: "DINGTALK", Name: "ding1", Enabled: true, ConfigJSON: `{"webhook_url":"` + ding.URL + `","secret":"SECtest"}`},
				{ChannelID: "slack1", Type: "SLACK", Name: "slack1", Enabled: true, ConfigJSON: `{"webhook_url":"` + slack.URL + `"}`},
				{ChannelID: "mail1", Type: "EMAIL", Name: "mail1", Enabled: true, ConfigJSON: fmt.Sprintf(`{"smtp_host":"%s","smtp_port":%d,"from":"a@b","to":["c@d"],"starttls":false}`, smtpHost, smtpPort)},
				{ChannelID: "web1", Type: "WEB", Name: "web1", Enabled: true, ConfigJSON: `{}`},
			}
		},
		Sender: &alert.ChannelSender{Sleep: func(time.Duration) {}, Attempts: 1},
	}
	if _, err := eng.Observe(context.Background(), alert.Event{
		Type: alert.TypeProcessExit, NodeID: id, ProcessID: "p-five",
		At: time.Now().UTC(), Firing: true,
	}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if hookN.Load() < 1 || wecomN.Load() < 1 || dingN.Load() < 1 || slackN.Load() < 1 {
		t.Fatalf("http hits hook=%d wecom=%d ding=%d slack=%d", hookN.Load(), wecomN.Load(), dingN.Load(), slackN.Load())
	}
	if smtpN.Load() < 1 {
		t.Fatalf("smtp hits=%d", smtpN.Load())
	}
}

func TestQ4_ListAlertsMarksUnreachableSTALE(t *testing.T) {
	addrA, _ := startClusterAgent(t, "")
	addrC, _, cancelC := startClusterAgentCtl(t, "")
	joinTwo(t, addrA, addrC)
	cancelC()
	out := mustCLI(t, addrA, "alert", "list")
	if !strings.Contains(strings.ToUpper(out), "STALE") {
		t.Fatalf("want STALE in aggregate list, got %s", out)
	}
}

func writeShortLivedSpec(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "boom.yaml")
	body := "name: boom\nprocess_id: boom\ncommand: /bin/sleep\nargs:\n  - \"1\"\ninstances: 1\nrestart:\n  mode: never\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitObservedLong(t *testing.T, addr, name string, want ...string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var listOut, errb string
	for time.Now().Before(deadline) {
		var code int
		code, listOut, errb = runP1CLI("--server", addr, "process", "list")
		if code == 0 {
			for _, obs := range want {
				if listHasObserved(listOut, name, obs) {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("wait observed %s %v: list=%q stderr=%q", name, want, listOut, errb)
}

func openAgentStore(t *testing.T, root string) *store.Store {
	t.Helper()
	st, err := store.Open(paths.New(root).Store)
	if err != nil {
		t.Fatalf("open store %s: %v", paths.New(root).Store, err)
	}
	return st
}

func alertHasFingerprint(t *testing.T, root, fp string) bool {
	t.Helper()
	st := openAgentStore(t, root)
	defer func() { _ = st.Close() }()
	_, err := st.GetAlertByFingerprint(context.Background(), fp)
	if err == nil {
		return true
	}
	if errcode.Is(err, errcode.NOT_FOUND) {
		return false
	}
	t.Fatalf("get fingerprint %s: %v", fp, err)
	return false
}

func startFakeSMTP(t *testing.T) (host string, port int, hits *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	hits = &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeSMTP(c, hits)
		}
	}()
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("smtp addr %T", ln.Addr())
	}
	return tcp.IP.String(), tcp.Port, hits
}

func serveFakeSMTP(c net.Conn, hits *atomic.Int32) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	write := func(s string) {
		_, _ = fmt.Fprintf(c, "%s\r\n", s)
	}
	write("220 localhost")
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				hits.Add(1)
				write("250 ok")
			}
			continue
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			write("250-localhost")
			write("250 OK")
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"), strings.HasPrefix(cmd, "RSET"), strings.HasPrefix(cmd, "NOOP"):
			write("250 ok")
		case strings.HasPrefix(cmd, "DATA"):
			write("354 go")
			inData = true
		case strings.HasPrefix(cmd, "QUIT"):
			write("221 bye")
			return
		default:
			write("250 ok")
		}
	}
}
