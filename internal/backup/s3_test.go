package backup_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/qleelulu/procmesh/internal/backup"
	"github.com/qleelulu/procmesh/internal/errcode"
)

type fakeS3 struct {
	*httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	f := &fakeS3{objects: make(map[string][]byte)}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(trimmed, "/")
	if bucket == "" {
		http.Error(w, "no bucket", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		prefix := r.URL.Query().Get("prefix")
		f.mu.Lock()
		var keys []string
		for k := range f.objects {
			if prefix == "" || strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		f.mu.Unlock()
		var b strings.Builder
		b.WriteString("<ListBucketResult>")
		for _, k := range keys {
			b.WriteString("<Contents><Key>")
			b.WriteString(k)
			b.WriteString("</Key></Contents>")
		}
		b.WriteString("</ListBucketResult>")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, b.String())
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.objects[key] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		f.mu.Lock()
		body, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	case http.MethodDelete:
		f.mu.Lock()
		_, ok := f.objects[key]
		if ok {
			delete(f.objects, key)
		}
		f.mu.Unlock()
		if !ok {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func testS3Config(fake *fakeS3) backup.S3Config {
	return backup.S3Config{
		Endpoint:  fake.URL,
		Bucket:    "b",
		Prefix:    "p",
		Region:    "us-east-1",
		AccessKey: "AK",
		SecretKey: "SK",
		Insecure:  true,
		ClusterID: "c",
		NodeID:    "n",
		HTTP:      fake.Client(),
	}
}

func TestS3Sink_PutGetListDeleteAgainstFake(t *testing.T) {
	fake := newFakeS3(t)
	cfg := backup.S3Config{
		Endpoint: fake.URL, Bucket: "b", Prefix: "p", Region: "us-east-1",
		AccessKey: "AK", SecretKey: "SK", Insecure: true,
		ClusterID: "c", NodeID: "n", HTTP: fake.Client(),
	}
	s, err := backup.NewS3Sink(cfg)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"format_version":1,"snapshot_id":"s1"}`)
	loc, err := s.Put(context.Background(), "s1", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loc, "s1.json") {
		t.Fatal(loc)
	}
	got, err := s.Get(context.Background(), "s1")
	if err != nil || string(got) != string(payload) {
		t.Fatalf("%s %v", got, err)
	}
	list, err := s.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	if err := s.Delete(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "s1"); !errcode.Is(err, errcode.NOT_FOUND) {
		t.Fatalf("err %v", err)
	}
}

func TestS3Sink_MissingBucketInvalid(t *testing.T) {
	_, err := backup.NewS3Sink(backup.S3Config{Endpoint: "http://x", AccessKey: "a", SecretKey: "b"})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err %v", err)
	}
}

func TestS3Sink_Name(t *testing.T) {
	s, _ := backup.NewS3Sink(backup.S3Config{Endpoint: "http://x", Bucket: "b", AccessKey: "a", SecretKey: "s", Insecure: true})
	if s.Name() != "s3" {
		t.Fatal("name")
	}
}

func TestS3Sink_MissingEndpointInvalid(t *testing.T) {
	_, err := backup.NewS3Sink(backup.S3Config{Bucket: "b", AccessKey: "a", SecretKey: "s"})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err %v", err)
	}
}

func TestS3Sink_HTTPRequiresInsecure(t *testing.T) {
	_, err := backup.NewS3Sink(backup.S3Config{
		Endpoint: "http://example.com", Bucket: "b", AccessKey: "a", SecretKey: "s",
	})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("err %v", err)
	}
}

func TestS3Sink_RejectsInvalidID(t *testing.T) {
	s, err := backup.NewS3Sink(testS3Config(newFakeS3(t)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(context.Background(), "../x", []byte("{}")); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("put err %v", err)
	}
	if _, err := s.Get(context.Background(), "a/b"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("get err %v", err)
	}
	if err := s.Delete(context.Background(), "x y"); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("delete err %v", err)
	}
}

func TestS3Sink_ServerErrorUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	s, err := backup.NewS3Sink(backup.S3Config{
		Endpoint: srv.URL, Bucket: "b", AccessKey: "a", SecretKey: "s",
		Insecure: true, ClusterID: "c", NodeID: "n", HTTP: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(context.Background(), "s1", []byte(`{}`)); !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("put err %v", err)
	}
	if _, err := s.Get(context.Background(), "s1"); !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("get err %v", err)
	}
	if _, err := s.List(context.Background()); !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("list err %v", err)
	}
	if err := s.Delete(context.Background(), "s1"); !errcode.Is(err, errcode.UNAVAILABLE) {
		t.Fatalf("delete err %v", err)
	}
}

func TestS3Sink_DoesNotLeakSecret(t *testing.T) {
	fake := newFakeS3(t)
	secret := "super-secret-key-xyz"
	s, err := backup.NewS3Sink(backup.S3Config{
		Endpoint: fake.URL, Bucket: "b", Prefix: "p", Region: "us-east-1",
		AccessKey: "AK", SecretKey: secret, Insecure: true,
		ClusterID: "c", NodeID: "n", HTTP: fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	loc, err := s.Put(context.Background(), "s1", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(loc, secret) {
		t.Fatalf("location leaked secret: %s", loc)
	}
	list, err := s.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	if strings.Contains(list[0].Location, secret) {
		t.Fatalf("list leaked secret: %s", list[0].Location)
	}
	_, err = s.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected missing get error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("err leaked secret: %v", err)
	}
}

func TestS3Sink_EmptyPrefixKey(t *testing.T) {
	fake := newFakeS3(t)
	s, err := backup.NewS3Sink(backup.S3Config{
		Endpoint: fake.URL, Bucket: "b", Prefix: "", Region: "us-east-1",
		AccessKey: "AK", SecretKey: "SK", Insecure: true,
		ClusterID: "c", NodeID: "n", HTTP: fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	loc, err := s.Put(context.Background(), "s1", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loc, "c/n/s1.json") {
		t.Fatal(loc)
	}
	got, err := s.Get(context.Background(), "s1")
	if err != nil || string(got) != "{}" {
		t.Fatalf("%s %v", got, err)
	}
	list, err := s.List(context.Background())
	if err != nil || len(list) != 1 || list[0].SnapshotID != "s1" {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestS3Sink_CanceledContext(t *testing.T) {
	s, err := backup.NewS3Sink(testS3Config(newFakeS3(t)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, "s1", []byte(`{}`)); err == nil {
		t.Fatal("put expected canceled")
	}
	if _, err := s.Get(ctx, "s1"); err == nil {
		t.Fatal("get expected canceled")
	}
	if _, err := s.List(ctx); err == nil {
		t.Fatal("list expected canceled")
	}
	if err := s.Delete(ctx, "s1"); err == nil {
		t.Fatal("delete expected canceled")
	}
}

func TestS3Sink_FakeRequiresSigV4(t *testing.T) {
	fake := newFakeS3(t)
	req, err := http.NewRequest(http.MethodGet, fake.URL+"/b/p/c/n/s1.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := fake.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestS3Config_Redacted(t *testing.T) {
	cfg := backup.S3Config{
		Endpoint:  "https://s3.example.com",
		Bucket:    "snaps",
		AccessKey: "AKIA",
		SecretKey: "secret",
	}
	got := cfg.Redacted()
	if got.AccessKey != "" || got.SecretKey != "" {
		t.Fatalf("secrets leaked: %+v", got)
	}
	if got.Bucket != "snaps" || got.Endpoint != "https://s3.example.com" {
		t.Fatalf("non-secret fields: %+v", got)
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		t.Fatal("Redacted must not mutate original")
	}
}
