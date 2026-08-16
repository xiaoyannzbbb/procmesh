package health

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"golang.org/x/sys/unix"
)

// HealthState is the probe outcome after thresholds are applied.
type HealthState string

const (
	HealthHealthy   HealthState = "HEALTHY"
	HealthUnhealthy HealthState = "UNHEALTHY"
	HealthUnknown   HealthState = "UNKNOWN"
)

// HealthCheckSpec is the per-process probe configuration.
type HealthCheckSpec struct {
	Type             string
	URL              string
	Method           string
	ExpectedStatus   int
	Address          string
	Command          string
	Args             []string
	InitialDelay     time.Duration
	Interval         time.Duration
	Timeout          time.Duration
	FailureThreshold int
	SuccessThreshold int
	RestartOnFailure bool
	RestartCooldown  time.Duration
}

const (
	defaultTimeout    = time.Second
	maxRedirects      = 5
	defaultHTTPMethod = "GET"
	defaultHTTPStatus = 200
)

// Check runs one probe. A nil error means healthy.
func Check(ctx context.Context, spec HealthCheckSpec, pid int) error {
	switch spec.Type {
	case "", "alive":
		return checkAlive(pid)
	case "http":
		return checkHTTP(ctx, spec)
	case "tcp":
		return checkTCP(ctx, spec)
	case "exec":
		return checkExec(ctx, spec)
	default:
		return errcode.E(errcode.INVALID, "health check type")
	}
}

func checkAlive(pid int) error {
	if pid <= 0 {
		return errcode.E(errcode.INVALID, "pid")
	}
	err := unix.Kill(pid, 0)
	if err == nil || err == unix.EPERM {
		return nil
	}
	return err
}

func checkHTTP(ctx context.Context, spec HealthCheckSpec) error {
	if err := allowHTTPURL(spec.URL); err != nil {
		return err
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	method := spec.Method
	if method == "" {
		method = defaultHTTPMethod
	}
	req, err := http.NewRequestWithContext(ctx, method, spec.URL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL != nil {
				if err := allowHTTPScheme(req.URL.Scheme); err != nil {
					return err
				}
			}
			if len(via) > maxRedirects {
				return errcode.E(errcode.INVALID, "too many redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	want := spec.ExpectedStatus
	if want == 0 {
		want = defaultHTTPStatus
	}
	if resp.StatusCode != want {
		return fmt.Errorf("status mismatch: got %d want %d", resp.StatusCode, want)
	}
	return nil
}

func allowHTTPURL(raw string) error {
	if raw == "" {
		return errcode.E(errcode.INVALID, "url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	return allowHTTPScheme(u.Scheme)
}

func allowHTTPScheme(scheme string) error {
	switch scheme {
	case "http", "https":
		return nil
	default:
		return errcode.E(errcode.INVALID, "url scheme")
	}
}

func checkTCP(ctx context.Context, spec HealthCheckSpec) error {
	if spec.Address == "" {
		return errcode.E(errcode.INVALID, "address")
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	d := net.Dialer{Timeout: timeout}
	return checkTCPWithDialer(ctx, spec.Address, &d)
}

type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

func checkTCPWithDialer(ctx context.Context, address string, dialer contextDialer) error {
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	// Dial success means healthy; Close errors are ignored.
	_ = conn.Close()
	return nil
}

func checkExec(ctx context.Context, spec HealthCheckSpec) error {
	if spec.Command == "" {
		return errcode.E(errcode.INVALID, "command")
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	return cmd.Run()
}

// Tracker applies consecutive failure/success thresholds.
type Tracker struct {
	failNeed int
	okNeed   int
	fails    int
	oks      int
	state    HealthState
}

// NewTracker starts in UNKNOWN until a threshold is met.
func NewTracker(spec HealthCheckSpec) *Tracker {
	failNeed := spec.FailureThreshold
	if failNeed <= 0 {
		failNeed = 1
	}
	okNeed := spec.SuccessThreshold
	if okNeed <= 0 {
		okNeed = 1
	}
	return &Tracker{
		failNeed: failNeed,
		okNeed:   okNeed,
		state:    HealthUnknown,
	}
}

// Observe records one check result and returns the resulting health state.
func (t *Tracker) Observe(err error, now time.Time) HealthState {
	if t == nil {
		return HealthUnknown
	}
	if err != nil {
		t.fails++
		t.oks = 0
		if t.fails >= t.failNeed {
			t.state = HealthUnhealthy
		}
		return t.state
	}
	t.oks++
	t.fails = 0
	if t.oks >= t.okNeed {
		t.state = HealthHealthy
	}
	return t.state
}

// SameThresholds reports whether t was built for spec's failure/success thresholds.
func (t *Tracker) SameThresholds(spec HealthCheckSpec) bool {
	if t == nil {
		return false
	}
	failNeed := spec.FailureThreshold
	if failNeed <= 0 {
		failNeed = 1
	}
	okNeed := spec.SuccessThreshold
	if okNeed <= 0 {
		okNeed = 1
	}
	return t.failNeed == failNeed && t.okNeed == okNeed
}
