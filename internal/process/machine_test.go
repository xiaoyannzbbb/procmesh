package process_test

import (
	"testing"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
)

func TestApplyObserved_HappyPathAndIllegal(t *testing.T) {
	s, err := process.ApplyObserved(process.ObservedStopped, process.EvStart)
	if err != nil || s != process.ObservedStarting {
		t.Fatalf("%s %v", s, err)
	}
	s, err = process.ApplyObserved(process.ObservedStarting, process.EvStartOK)
	if err != nil || s != process.ObservedRunning {
		t.Fatalf("%s %v", s, err)
	}
	_, err = process.ApplyObserved(process.ObservedStopped, process.EvStop)
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("want INVALID got %v", err)
	}
}

func TestApplyObserved_LegalTransitions(t *testing.T) {
	tests := []struct {
		cur  process.ObservedState
		ev   process.Event
		next process.ObservedState
	}{
		{process.ObservedStopped, process.EvStart, process.ObservedStarting},
		{process.ObservedStarting, process.EvStartOK, process.ObservedRunning},
		{process.ObservedStarting, process.EvStartFail, process.ObservedBackoff},
		{process.ObservedRunning, process.EvExit, process.ObservedExited},
		{process.ObservedRunning, process.EvStop, process.ObservedStopping},
		{process.ObservedStopping, process.EvStopped, process.ObservedStopped},
		{process.ObservedExited, process.EvRetry, process.ObservedStarting},
		{process.ObservedExited, process.EvRetriesExhausted, process.ObservedFatal},
		{process.ObservedBackoff, process.EvRetry, process.ObservedStarting},
		{process.ObservedBackoff, process.EvRetriesExhausted, process.ObservedFatal},
		{process.ObservedFatal, process.EvStart, process.ObservedStarting},
		{process.ObservedUnknown, process.EvStartOK, process.ObservedRunning},
		{process.ObservedUnknown, process.EvStopped, process.ObservedStopped},
		{process.ObservedStopped, process.EvLost, process.ObservedUnknown},
		{process.ObservedStarting, process.EvLost, process.ObservedUnknown},
		{process.ObservedRunning, process.EvLost, process.ObservedUnknown},
		{process.ObservedStopping, process.EvLost, process.ObservedUnknown},
		{process.ObservedExited, process.EvLost, process.ObservedUnknown},
		{process.ObservedBackoff, process.EvLost, process.ObservedUnknown},
		{process.ObservedFatal, process.EvLost, process.ObservedUnknown},
		{process.ObservedUnknown, process.EvLost, process.ObservedUnknown},
	}
	for _, tt := range tests {
		t.Run(string(tt.cur)+"+"+string(tt.ev), func(t *testing.T) {
			got, err := process.ApplyObserved(tt.cur, tt.ev)
			if err != nil {
				t.Fatalf("err %v", err)
			}
			if got != tt.next {
				t.Fatalf("got %s want %s", got, tt.next)
			}
		})
	}
}

func TestApplyObserved_IllegalTransitions(t *testing.T) {
	tests := []struct {
		cur process.ObservedState
		ev  process.Event
	}{
		{process.ObservedStopped, process.EvStop},
		{process.ObservedStopped, process.EvExit},
		{process.ObservedStopped, process.EvStartOK},
		{process.ObservedStopped, process.EvRetriesExhausted},
		{process.ObservedRunning, process.EvStart},
		{process.ObservedRunning, process.EvStartOK},
		{process.ObservedStarting, process.EvStop},
		{process.ObservedStarting, process.EvExit},
		{process.ObservedStarting, process.EvStopped},
		{process.ObservedExited, process.EvStart},
		{process.ObservedExited, process.EvStartOK},
		{process.ObservedExited, process.EvStartFail},
		{process.ObservedBackoff, process.EvStop},
		{process.ObservedBackoff, process.EvExit},
		{process.ObservedBackoff, process.EvStart},
		{process.ObservedBackoff, process.EvStartOK},
		{process.ObservedFatal, process.EvStop},
		{process.ObservedFatal, process.EvExit},
		{process.ObservedFatal, process.EvStartFail},
		{process.ObservedFatal, process.EvStopped},
		{process.ObservedUnknown, process.EvStart},
		{process.ObservedUnknown, process.EvStartFail},
		{process.ObservedUnknown, process.EvStop},
		{process.ObservedUnknown, process.EvExit},
		{process.ObservedUnknown, process.EvRetry},
		{process.ObservedUnknown, process.EvRetriesExhausted},
	}
	for _, tt := range tests {
		t.Run(string(tt.cur)+"+"+string(tt.ev), func(t *testing.T) {
			_, err := process.ApplyObserved(tt.cur, tt.ev)
			if !errcode.Is(err, errcode.INVALID) {
				t.Fatalf("want INVALID got %v", err)
			}
		})
	}
}
