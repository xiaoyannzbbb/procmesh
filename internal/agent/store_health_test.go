package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStoreHealthMonitorReadyDoesNotRerunSQLiteChecks(t *testing.T) {
	var fullCalls, quickCalls, headerCalls int
	m := newStoreHealthMonitor(
		func(context.Context) error { fullCalls++; return nil },
		func(context.Context) error { quickCalls++; return nil },
		func() error { headerCalls++; return nil },
	)

	if err := m.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if err := m.ready(); err != nil {
			t.Fatal(err)
		}
	}
	if fullCalls != 1 || quickCalls != 0 || headerCalls != 101 {
		t.Fatalf("checks full=%d quick=%d header=%d", fullCalls, quickCalls, headerCalls)
	}
}

func TestStoreHealthMonitorQuickFailureRecovers(t *testing.T) {
	var quickErr error
	m := newStoreHealthMonitor(
		func(context.Context) error { return nil },
		func(context.Context) error { return quickErr },
		func() error { return nil },
	)
	if err := m.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}

	quickErr = errors.New("quick failed")
	m.checkQuick(context.Background())
	if err := m.ready(); err == nil {
		t.Fatal("ready after quick-check failure")
	}

	quickErr = nil
	m.checkQuick(context.Background())
	if err := m.ready(); err != nil {
		t.Fatalf("ready did not recover: %v", err)
	}
}

func TestStoreHealthMonitorFullFailureRequiresFullRecovery(t *testing.T) {
	var fullErr error
	m := newStoreHealthMonitor(
		func(context.Context) error { return fullErr },
		func(context.Context) error { return nil },
		func() error { return nil },
	)
	if err := m.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}

	fullErr = errors.New("full failed")
	m.checkFull(context.Background())
	m.checkQuick(context.Background())
	if err := m.ready(); err == nil {
		t.Fatal("quick-check cleared full-check failure")
	}

	fullErr = nil
	m.checkFull(context.Background())
	if err := m.ready(); err != nil {
		t.Fatalf("full-check recovery did not clear failure: %v", err)
	}
}

func TestStoreHealthMonitorHeaderFailureRecovers(t *testing.T) {
	var headerErr = errors.New("header failed")
	m := newStoreHealthMonitor(
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
		func() error { return headerErr },
	)
	if err := m.initialize(context.Background()); err != nil {
		t.Fatalf("header failure latched as startup integrity failure: %v", err)
	}
	if err := m.ready(); err == nil {
		t.Fatal("ready after header failure")
	}

	headerErr = nil
	m.checkQuick(context.Background())
	if err := m.ready(); err != nil {
		t.Fatalf("header recovery did not clear failure: %v", err)
	}
}

func TestStoreHealthMonitorRunDispatchesTicks(t *testing.T) {
	quickCalled := make(chan struct{}, 1)
	fullCalled := make(chan struct{}, 1)
	m := newStoreHealthMonitor(
		func(context.Context) error { fullCalled <- struct{}{}; return nil },
		func(context.Context) error { quickCalled <- struct{}{}; return nil },
		func() error { return nil },
	)
	quickTicks := make(chan time.Time, 1)
	fullTicks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.run(ctx, quickTicks, fullTicks)
		close(done)
	}()

	quickTicks <- time.Now()
	select {
	case <-quickCalled:
	case <-time.After(time.Second):
		t.Fatal("quick-check was not dispatched")
	}
	fullTicks <- time.Now()
	select {
	case <-fullCalled:
	case <-time.After(time.Second):
		t.Fatal("full-check was not dispatched")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop")
	}
}

func TestCheckSQLiteHeaderReadsFixedHeader(t *testing.T) {
	r := &sizedReader{data: []byte("SQLite format 3\x00trailing data")}
	if err := checkSQLiteHeader(r); err != nil {
		t.Fatal(err)
	}
	if r.maxRead != sqliteHeaderSize {
		t.Fatalf("largest read=%d want %d", r.maxRead, sqliteHeaderSize)
	}
	if r.totalRead != sqliteHeaderSize {
		t.Fatalf("total read=%d want %d", r.totalRead, sqliteHeaderSize)
	}

	for _, input := range []string{"short", "not sqlite data!"} {
		if err := checkSQLiteHeader(strings.NewReader(input)); err == nil {
			t.Fatalf("checkSQLiteHeader(%q) succeeded", input)
		}
	}
}

type sizedReader struct {
	data      []byte
	maxRead   int
	totalRead int
}

func (r *sizedReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		r.maxRead = len(p)
	}
	if len(r.data) == 0 {
		return 0, errors.New("read past available data")
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	r.totalRead += n
	return n, nil
}
