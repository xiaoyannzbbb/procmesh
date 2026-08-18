package alert

import (
	"context"
	"encoding/json"
	"time"

	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/store"
)

type Event struct {
	Type                         Type
	NodeID, ProcessID, ClusterID string
	Payload                      map[string]any
	At                           time.Time
	Firing                       bool
}

type Sender interface {
	Send(ctx context.Context, ch control.AlertChannel, rec store.AlertRecord) error
}

type Engine struct {
	Store       *store.Store
	NodeID      string
	NewID       func() string
	Policy      func() control.AlertPolicy
	Channels    func() []control.AlertChannel
	Sender      Sender
	Audit       func(action, result, meta string) // 可空
	OnSendError func(ch control.AlertChannel, err error)
	Now         func() time.Time
}

func (e *Engine) Observe(ctx context.Context, ev Event) (store.AlertRecord, error) {
	if requiresProcessID(ev.Type) && ev.ProcessID == "" {
		return store.AlertRecord{}, errcode.E(errcode.INVALID, "process_id")
	}

	fp := Fingerprint(ev.Type, ev.NodeID, ev.ProcessID, ev.ClusterID)
	rec, err := e.Store.GetAlertByFingerprint(ctx, fp)
	if err != nil {
		if !errcode.Is(err, errcode.NOT_FOUND) {
			return store.AlertRecord{}, err
		}
		id := ""
		if e.NewID != nil {
			id = e.NewID()
		}
		rec = store.AlertRecord{
			AlertID:     id,
			Fingerprint: fp,
			FirstAt:     ev.At,
		}
	}

	rec.Type = string(ev.Type)
	rec.Severity = string(DefaultSeverity(ev.Type))
	rec.NodeID = ev.NodeID
	rec.ProcessID = ev.ProcessID
	rec.PayloadJSON = marshalPayload(ev.Payload)

	notify := false
	if ev.Firing {
		rec.State = string(StateFiring)
		rec.LastAt = ev.At
		rec.ResolvedAt = time.Time{}
		notify = true
	} else if rec.State == string(StateResolved) {
		rec.LastAt = ev.At
	} else {
		rec.State = string(StateResolved)
		rec.LastAt = ev.At
		rec.ResolvedAt = ev.At
		notify = e.policy().NotifyOnResolve
	}

	// Inbox first so a hung Send cannot drop the FIRING/RESOLVED row.
	if err := e.Store.UpsertAlert(ctx, rec); err != nil {
		return store.AlertRecord{}, err
	}
	if notify {
		e.dispatch(ctx, &rec, ev)
		if err := e.Store.UpsertAlert(ctx, rec); err != nil {
			return store.AlertRecord{}, err
		}
	}
	return rec, nil
}

func (e *Engine) dispatch(ctx context.Context, rec *store.AlertRecord, ev Event) {
	if e.Sender == nil {
		return
	}
	window := time.Duration(e.policy().DedupWindowSec) * time.Second
	if !rec.NotifiedAt.IsZero() && ev.At.Sub(rec.NotifiedAt) < window {
		return
	}

	var lastErr error
	attempted := false
	anyOK := false
	for _, ch := range e.channels() {
		if !ch.Enabled || ch.Type == "WEB" {
			continue
		}
		attempted = true
		if err := e.Sender.Send(ctx, ch, *rec); err != nil {
			lastErr = err
			if e.OnSendError != nil {
				e.OnSendError(ch, err)
			}
			if e.Audit != nil {
				e.Audit("alert.send", "error", ch.ChannelID)
			}
			continue
		}
		anyOK = true
		if e.Audit != nil {
			e.Audit("alert.send", "ok", ch.ChannelID)
		}
	}
	if anyOK {
		rec.NotifiedAt = ev.At
		rec.LastError = ""
		return
	}
	if attempted && lastErr != nil {
		rec.LastError = lastErr.Error()
	}
}

func (e *Engine) policy() control.AlertPolicy {
	if e.Policy == nil {
		return control.DefaultAlertPolicy()
	}
	return e.Policy()
}

func (e *Engine) channels() []control.AlertChannel {
	if e.Channels == nil {
		return nil
	}
	return e.Channels()
}

func marshalPayload(p map[string]any) string {
	if len(p) == 0 {
		return "{}"
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(b)
}
