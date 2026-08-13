package process

import "github.com/qleelulu/procmesh/internal/errcode"

// Event is an observed-state machine input.
type Event string

const (
	EvStart            Event = "START"
	EvStartOK          Event = "START_OK"
	EvStartFail        Event = "START_FAIL"
	EvExit             Event = "EXIT"
	EvStop             Event = "STOP"
	EvStopped          Event = "STOPPED"
	EvRetry            Event = "RETRY"
	EvRetriesExhausted Event = "RETRIES_EXHAUSTED"
	EvLost             Event = "LOST"
)

var observedTransitions = map[[2]string]ObservedState{
	{string(ObservedStopped), string(EvStart)}:              ObservedStarting,
	{string(ObservedStarting), string(EvStartOK)}:           ObservedRunning,
	{string(ObservedStarting), string(EvStartFail)}:         ObservedBackoff,
	{string(ObservedRunning), string(EvExit)}:               ObservedExited,
	{string(ObservedRunning), string(EvStop)}:               ObservedStopping,
	{string(ObservedStopping), string(EvStopped)}:           ObservedStopped,
	{string(ObservedExited), string(EvRetry)}:               ObservedStarting,
	{string(ObservedExited), string(EvRetriesExhausted)}:    ObservedFatal,
	{string(ObservedBackoff), string(EvRetry)}:              ObservedStarting,
	{string(ObservedBackoff), string(EvRetriesExhausted)}:   ObservedFatal,
	{string(ObservedFatal), string(EvStart)}:                ObservedStarting,
	{string(ObservedUnknown), string(EvStartOK)}:            ObservedRunning,
	{string(ObservedUnknown), string(EvStopped)}:            ObservedStopped,
	{string(ObservedStopped), string(EvLost)}:               ObservedUnknown,
	{string(ObservedStarting), string(EvLost)}:              ObservedUnknown,
	{string(ObservedRunning), string(EvLost)}:               ObservedUnknown,
	{string(ObservedStopping), string(EvLost)}:              ObservedUnknown,
	{string(ObservedExited), string(EvLost)}:                ObservedUnknown,
	{string(ObservedBackoff), string(EvLost)}:               ObservedUnknown,
	{string(ObservedFatal), string(EvLost)}:                 ObservedUnknown,
	{string(ObservedUnknown), string(EvLost)}:               ObservedUnknown,
}

// ApplyObserved returns the next observed state for a legal (cur, ev) pair.
// Illegal pairs return errcode.INVALID and leave the caller to keep cur.
func ApplyObserved(cur ObservedState, ev Event) (ObservedState, error) {
	next, ok := observedTransitions[[2]string{string(cur), string(ev)}]
	if !ok {
		return "", errcode.E(errcode.INVALID, "illegal observed transition")
	}
	return next, nil
}
