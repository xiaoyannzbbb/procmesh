package errcode

import (
	"errors"
	"testing"
)

func TestWrapPreservesCauseWithoutExposingIt(t *testing.T) {
	cause := errors.New("internal address 10.0.0.9")
	err := Wrap(UNAVAILABLE, "leader is unknown", cause)

	if !errors.Is(err, cause) {
		t.Fatalf("cause was lost: %v", err)
	}
	if got, want := err.Error(), "UNAVAILABLE: leader is unknown"; got != want {
		t.Fatalf("Error()=%q want %q", got, want)
	}
}
