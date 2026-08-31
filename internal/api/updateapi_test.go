package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/update"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

type stubLatestChecker struct {
	res update.Result
	err error
}

func (s stubLatestChecker) CheckLatest(context.Context, bool) (update.Result, error) {
	return s.res, s.err
}

func TestUpdateAPI_CheckLatestDeniedWithoutClusterRead(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	applyAuthCmd(t, svc, control.CmdUserPut, control.UserPutBody{
		ID: "user-noperm", Username: "noperm", PasswordHash: testAdminHash(t),
	})
	applyAuthCmd(t, svc, control.CmdRolePut, control.RolePutBody{
		ID: "no-cluster", Name: "no-cluster", Perms: []string{auth.PermProcessRead},
	})
	applyAuthCmd(t, svc, control.CmdBindPut, control.BindPutBody{
		UserID: "user-noperm", RoleID: "no-cluster", Scope: control.ScopeCluster,
	})

	api := &UpdateAPI{
		Auth: svc,
		Checker: stubLatestChecker{res: update.Result{
			Pin: update.Pin{Repository: "o/r", Tag: "v1.0.0", Checksums: map[string]string{"linux/amd64": "x"}},
		}},
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-noperm", Username: "noperm"})
	_, err := api.CheckLatest(ctx, connect.NewRequest(&procmeshv1.CheckLatestRequest{}))
	assertDenied(t, err)
}

func TestUpdateAPI_CheckLatestAuthNilAllows(t *testing.T) {
	api := &UpdateAPI{
		Checker: stubLatestChecker{res: update.Result{
			Pin: update.Pin{
				Repository: "xiaoyannzbbb/procmesh",
				Tag:        "v0.2.0",
				Checksums: map[string]string{
					"linux/amd64": "aaa",
					"linux/arm64": "bbb",
					"linux/armv7": "ccc",
				},
			},
			CheckedUnixMs: 1_700_000_000_000,
		}},
	}
	got, err := api.CheckLatest(context.Background(), connect.NewRequest(&procmeshv1.CheckLatestRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetRepository() != "xiaoyannzbbb/procmesh" || got.Msg.GetTag() != "v0.2.0" {
		t.Fatalf("%+v", got.Msg)
	}
}

func TestUpdateAPI_CheckLatestMapsPinFields(t *testing.T) {
	_, svc := newBootstrappedAuth(t)
	putViewerUser(t, svc)
	api := &UpdateAPI{
		Auth: svc,
		Checker: stubLatestChecker{res: update.Result{
			Pin: update.Pin{
				Repository: "fork/procmesh",
				Tag:        "v0.9.1",
				Checksums: map[string]string{
					"linux/amd64": "hash-amd64",
					"linux/arm64": "hash-arm64",
					"linux/armv7": "hash-armv7",
				},
			},
			CheckedUnixMs: 42,
			FromCache:     true,
			CheckError:    true,
			ErrorMessage:  "github down",
		}},
	}
	ctx := WithPrincipal(context.Background(), auth.Principal{UserID: "user-view", Username: "viewer"})
	got, err := api.CheckLatest(ctx, connect.NewRequest(&procmeshv1.CheckLatestRequest{Refresh: true}))
	if err != nil {
		t.Fatal(err)
	}
	msg := got.Msg
	if msg.GetRepository() != "fork/procmesh" || msg.GetTag() != "v0.9.1" {
		t.Fatalf("pin identity %+v", msg)
	}
	if msg.GetChecksums()["linux/amd64"] != "hash-amd64" ||
		msg.GetChecksums()["linux/arm64"] != "hash-arm64" ||
		msg.GetChecksums()["linux/armv7"] != "hash-armv7" {
		t.Fatalf("checksums=%v", msg.GetChecksums())
	}
	if msg.GetCheckedUnixMs() != 42 || !msg.GetFromCache() || !msg.GetCheckError() || msg.GetErrorMessage() != "github down" {
		t.Fatalf("meta %+v", msg)
	}
}

func TestUpdateAPI_CheckLatestDoesNotExposeURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	api := &UpdateAPI{
		Checker: update.NewChecker(update.GitHubSource{
			Repository:   "owner/procmesh",
			APIBase:      base,
			DownloadBase: base,
		}, time.Now),
	}
	_, err := api.CheckLatest(context.Background(), connect.NewRequest(&procmeshv1.CheckLatestRequest{}))
	if err == nil {
		t.Fatal("expected error")
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg := e.Error()
		if strings.Contains(msg, "://") || strings.Contains(msg, base) || strings.Contains(msg, "github.com") {
			t.Fatalf("url leaked in API error: %q", msg)
		}
	}
}
