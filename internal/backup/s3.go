package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// S3Config configures a path-style S3-compatible snapshot sink.
type S3Config struct {
	Endpoint  string
	Bucket    string
	Prefix    string
	Region    string
	AccessKey string
	SecretKey string
	Insecure  bool // 仅测试
	ClusterID string
	NodeID    string
	HTTP      *http.Client // 测试注入
}

// S3Sink stores snapshot payloads on an S3-compatible endpoint.
type S3Sink struct {
	cfg S3Config
}

// NewS3Sink validates cfg and returns an S3 Sink.
func NewS3Sink(cfg S3Config) (*S3Sink, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errcode.E(errcode.INVALID, "s3 endpoint and bucket required")
	}
	if !cfg.Insecure && strings.HasPrefix(strings.ToLower(cfg.Endpoint), "http://") {
		return nil, errcode.E(errcode.INVALID, "s3 endpoint must be https")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" {
		return nil, errcode.E(errcode.INVALID, "invalid s3 endpoint")
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	return &S3Sink{cfg: cfg}, nil
}

// Name returns the sink identifier "s3".
func (s *S3Sink) Name() string { return "s3" }

func (s *S3Sink) validateID(id string) error {
	if !snapshotIDRe.MatchString(id) {
		return errcode.E(errcode.INVALID, "invalid snapshot id")
	}
	return nil
}

func (s *S3Sink) objectKey(id string) string {
	return path.Join(s.cfg.Prefix, s.cfg.ClusterID, s.cfg.NodeID, id+".json")
}

func (s *S3Sink) objectPath(id string) string {
	return "/" + s.cfg.Bucket + "/" + s.objectKey(id)
}

func (s *S3Sink) listPrefix() string {
	p := path.Join(s.cfg.Prefix, s.cfg.ClusterID, s.cfg.NodeID)
	if p == "" {
		return ""
	}
	return p + "/"
}

func (s *S3Sink) location(id string) string {
	return "s3://" + s.cfg.Bucket + "/" + s.objectKey(id)
}

// Put writes payload to {prefix}/{cluster}/{node}/{id}.json.
func (s *S3Sink) Put(ctx context.Context, id string, payload []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.validateID(id); err != nil {
		return "", err
	}
	if _, err := s.do(ctx, http.MethodPut, s.objectPath(id), nil, payload); err != nil {
		return "", err
	}
	return s.location(id), nil
}

// Get reads a snapshot payload by id.
func (s *S3Sink) Get(ctx context.Context, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.validateID(id); err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodGet, s.objectPath(id), nil, nil)
}

// List enumerates snapshot objects under the configured prefix.
func (s *S3Sink) List(ctx context.Context) ([]Listed, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("prefix", s.listPrefix())
	data, err := s.do(ctx, http.MethodGet, "/"+s.cfg.Bucket, q, nil)
	if err != nil {
		return nil, err
	}
	var result listBucketResult
	if err := xml.Unmarshal(data, &result); err != nil {
		return nil, errcode.E(errcode.UNAVAILABLE, "s3 list xml")
	}
	base := path.Join(s.cfg.Prefix, s.cfg.ClusterID, s.cfg.NodeID)
	out := make([]Listed, 0, len(result.Contents))
	for _, c := range result.Contents {
		id, ok := snapshotIDFromKey(c.Key, base)
		if !ok {
			continue
		}
		out = append(out, Listed{SnapshotID: id, Location: s.location(id)})
	}
	return out, nil
}

// Delete removes a snapshot object by id.
func (s *S3Sink) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateID(id); err != nil {
		return err
	}
	_, err := s.do(ctx, http.MethodDelete, s.objectPath(id), nil, nil)
	return err
}

type listBucketResult struct {
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

func snapshotIDFromKey(key, base string) (string, bool) {
	name := key
	if base != "" {
		prefix := base + "/"
		if !strings.HasPrefix(key, prefix) {
			return "", false
		}
		name = strings.TrimPrefix(key, prefix)
	}
	if name == "" || strings.Contains(name, "/") || !strings.HasSuffix(name, ".json") {
		return "", false
	}
	id := strings.TrimSuffix(name, ".json")
	if !snapshotIDRe.MatchString(id) {
		return "", false
	}
	return id, true
}

func (s *S3Sink) do(ctx context.Context, method, urlPath string, query url.Values, body []byte) ([]byte, error) {
	req, err := s.newRequest(ctx, method, urlPath, query, body)
	if err != nil {
		return nil, err
	}
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errcode.E(errcode.UNAVAILABLE, "s3 request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errcode.E(errcode.UNAVAILABLE, "s3 read failed")
	}
	if err := s3StatusErr(resp.StatusCode); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *S3Sink) newRequest(ctx context.Context, method, urlPath string, query url.Values, body []byte) (*http.Request, error) {
	u, err := url.Parse(s.cfg.Endpoint)
	if err != nil || u.Host == "" {
		return nil, errcode.E(errcode.INVALID, "invalid s3 endpoint")
	}
	u.Path = urlPath
	u.RawQuery = encodeAWSQuery(query)
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return nil, fmt.Errorf("s3 request: %w", err)
	}
	signV4(req, body, s.cfg.AccessKey, s.cfg.SecretKey, s.cfg.Region, time.Now())
	return req, nil
}

func s3StatusErr(code int) error {
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusNotFound:
		return errcode.E(errcode.NOT_FOUND, "snapshot not found")
	case code >= 500:
		return errcode.E(errcode.UNAVAILABLE, "s3 unavailable")
	default:
		return errcode.E(errcode.UNAVAILABLE, "s3 status "+strconv.Itoa(code))
	}
}

func signV4(req *http.Request, payload []byte, accessKey, secretKey, region string, now time.Time) {
	if payload == nil {
		payload = []byte{}
	}
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := hex.EncodeToString(hashSHA256(payload))

	host := req.URL.Host
	req.Host = host
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalRequest := req.Method + "\n" +
		canonicalURI + "\n" +
		req.URL.RawQuery + "\n" +
		canonicalHeaders + "\n" +
		signedHeaders + "\n" +
		payloadHash

	scope := dateStamp + "/" + region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" +
		amzDate + "\n" +
		scope + "\n" +
		hex.EncodeToString(hashSHA256([]byte(canonicalRequest)))

	sig := hex.EncodeToString(hmacSHA256(deriveSigningKey(secretKey, dateStamp, region, "s3"), stringToSign))
	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+
			", SignedHeaders="+signedHeaders+
			", Signature="+sig)
}

func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(data))
	return m.Sum(nil)
}

func hashSHA256(p []byte) []byte {
	sum := sha256.Sum256(p)
	return sum[:]
}

func encodeAWSQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		ek := uriEncode(k, true)
		for _, v := range q[k] {
			parts = append(parts, ek+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' || (c == '/' && !encodeSlash) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

var _ Sink = (*S3Sink)(nil)
