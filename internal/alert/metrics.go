package alert

import (
	"sync"
	"sync/atomic"
)

var sendTotals sync.Map // type\x00result -> *atomic.Uint64

// RecordSend increments the outbound send counter for a channel type and result.
func RecordSend(typ, result string) {
	if typ == "" || typ == "WEB" {
		return
	}
	if result != "ok" && result != "error" {
		result = "error"
	}
	key := typ + "\x00" + result
	v, _ := sendTotals.LoadOrStore(key, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}

// SendTotal is one labeled counter sample for /metrics.
type SendTotal struct {
	Type, Result string
	N            uint64
}

// SendTotals returns a snapshot of outbound send attempts.
func SendTotals() []SendTotal {
	out := make([]SendTotal, 0, 8)
	sendTotals.Range(func(key, val any) bool {
		k, ok := key.(string)
		if !ok {
			return true
		}
		n, ok := val.(*atomic.Uint64)
		if !ok || n == nil {
			return true
		}
		typ, result, found := splitSendKey(k)
		if !found {
			return true
		}
		out = append(out, SendTotal{Type: typ, Result: result, N: n.Load()})
		return true
	})
	return out
}

func splitSendKey(k string) (typ, result string, ok bool) {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[:i], k[i+1:], true
		}
	}
	return "", "", false
}
