package alert_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/alert"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/store"
)

type recordingSender struct {
	n    int
	errs map[string]error
	chs  []control.AlertChannel
	recs []store.AlertRecord
}

func (s *recordingSender) Send(_ context.Context, ch control.AlertChannel, rec store.AlertRecord) error {
	s.n++
	s.chs = append(s.chs, ch)
	s.recs = append(s.recs, rec)
	if s.errs != nil {
		if err, ok := s.errs[ch.ChannelID]; ok {
			return err
		}
	}
	return nil
}

func TestEngine_ReuseRowAndDedupWindow(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	snd := &recordingSender{}
	now := time.Unix(1_700_000_000, 0).UTC()
	eng := &alert.Engine{
		Store: st, NodeID: "n1", NewID: func() string { return "a1" },
		Policy: func() control.AlertPolicy { return control.DefaultAlertPolicy() },
		Channels: func() []control.AlertChannel {
			return []control.AlertChannel{{
				ChannelID: "c1", Type: "WEBHOOK", Name: "h", Enabled: true, ConfigJSON: `{"url":"http://x"}`,
			}}
		},
		Sender: snd,
		Now:    func() time.Time { return now },
	}
	ev := alert.Event{Type: alert.TypeProcessExit, NodeID: "n1", ProcessID: "p1", At: now, Firing: true}
	r1, err := eng.Observe(ctx, ev)
	if err != nil || r1.AlertID != "a1" || snd.n != 1 {
		t.Fatalf("first %+v n=%d err=%v", r1, snd.n, err)
	}
	ev.At = now.Add(time.Minute)
	r2, err := eng.Observe(ctx, ev)
	if err != nil || r2.AlertID != "a1" || snd.n != 1 {
		t.Fatalf("dedup %+v n=%d err=%v", r2, snd.n, err)
	}
	ev.At = now.Add(11 * time.Minute)
	if _, err := eng.Observe(ctx, ev); err != nil || snd.n != 2 {
		t.Fatalf("window n=%d err=%v", snd.n, err)
	}
}

func TestEngine_InboxWithoutOutboundStillWrites(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := &alert.Engine{
		Store: st, NewID: func() string { return "a2" },
		Policy:   func() control.AlertPolicy { return control.DefaultAlertPolicy() },
		Channels: func() []control.AlertChannel { return []control.AlertChannel{} },
		Sender:   &recordingSender{},
		Now:      func() time.Time { return time.Unix(1, 0).UTC() },
	}
	r, err := eng.Observe(ctx, alert.Event{Type: alert.TypeProcessFatal, NodeID: "n1", ProcessID: "p1", At: time.Unix(1, 0).UTC(), Firing: true})
	if err != nil || r.State != string(alert.StateFiring) {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestEngine_ResolveRespectsNotifyOnResolve(t *testing.T) {
	// NotifyOnResolve=false：FIRING 发 1 次，RESOLVED 不再 Send
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	snd := &recordingSender{}
	now := time.Unix(1_700_000_000, 0).UTC()
	pol := control.DefaultAlertPolicy()
	pol.NotifyOnResolve = false
	eng := &alert.Engine{
		Store: st, NodeID: "n1", NewID: func() string { return "a3" },
		Policy: func() control.AlertPolicy { return pol },
		Channels: func() []control.AlertChannel {
			return []control.AlertChannel{{
				ChannelID: "c1", Type: "WEBHOOK", Name: "h", Enabled: true, ConfigJSON: `{"url":"http://x"}`,
			}}
		},
		Sender: snd,
		Now:    func() time.Time { return now },
	}
	ev := alert.Event{Type: alert.TypeProcessExit, NodeID: "n1", ProcessID: "p1", At: now, Firing: true}
	r1, err := eng.Observe(ctx, ev)
	if err != nil || r1.State != string(alert.StateFiring) || snd.n != 1 {
		t.Fatalf("firing %+v n=%d err=%v", r1, snd.n, err)
	}
	ev.Firing = false
	ev.At = now.Add(time.Second)
	r2, err := eng.Observe(ctx, ev)
	if err != nil || r2.State != string(alert.StateResolved) || snd.n != 1 {
		t.Fatalf("resolved %+v n=%d err=%v", r2, snd.n, err)
	}
}
