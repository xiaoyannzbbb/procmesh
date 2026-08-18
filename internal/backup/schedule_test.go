package backup_test

import (
	"context"
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
)

func TestNext_Hourly(t *testing.T) {
	from := time.Date(2026, 8, 16, 10, 15, 0, 0, time.UTC)
	got, err := backup.Next("0 * * * *", from)
	if err != nil || !got.Equal(time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestNext_EmptyDisabled(t *testing.T) {
	tm, err := backup.Next("", time.Now())
	if err != nil || !tm.IsZero() {
		t.Fatalf("%v %v", tm, err)
	}
}

func TestNext_Invalid(t *testing.T) {
	_, err := backup.Next("0 * *", time.Now())
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("fields err %v", err)
	}
	_, err = backup.Next("60 * * * *", time.Now())
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("range err %v", err)
	}
	_, err = backup.Next("x * * * *", time.Now())
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("token err %v", err)
	}
}

func TestTickSchedule_EmptyDisabled(t *testing.T) {
	e := seededEngine(t)
	e.Schedule = ""
	if err := e.TickSchedule(context.Background()); err != nil {
		t.Fatal(err)
	}
	list, err := e.ListLocal(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("disabled schedule must not create: %+v %v", list, err)
	}
}

func TestTickSchedule_FiresWhenDue(t *testing.T) {
	e := seededEngine(t)
	e.Schedule = "0 * * * *"
	e.Now = func() time.Time { return time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC) }
	if err := e.TickSchedule(context.Background()); err != nil {
		t.Fatal(err)
	}
	list, err := e.ListLocal(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("due tick should create: %+v %v", list, err)
	}
}
