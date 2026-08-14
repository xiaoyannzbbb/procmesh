package localhttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/process"
)

const replayHeader = "X-Idempotent-Replay"

type Server struct {
	*http.Server
	mgr      *process.Manager
	logs     *logmgr.Manager
	degraded bool
	ready    func() error
}

func NewServer(mgr *process.Manager, logs *logmgr.Manager, addr string) (*Server, error) {
	return NewServerOpts(mgr, logs, addr, false, nil)
}

func NewServerOpts(mgr *process.Manager, logs *logmgr.Manager, addr string, degraded bool, ready func() error) (*Server, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	s := &Server{
		Server:   &http.Server{Addr: addr},
		mgr:      mgr,
		logs:     logs,
		degraded: degraded,
		ready:    ready,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/readyz", s.readyz)
	mux.HandleFunc("/v1/processes", s.processes)
	mux.HandleFunc("/v1/processes/", s.processSub)
	mux.HandleFunc("/v1/instances/", s.instanceSub)
	s.Server.Handler = mux
	return s, nil
}

func (s *Server) SetDegraded(v bool) { s.degraded = v }

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.isDegraded() {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "DEGRADED")
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (s *Server) isDegraded() bool {
	if s.degraded {
		return true
	}
	if s.ready != nil && s.ready() != nil {
		return true
	}
	return false
}

func (s *Server) processes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listProcesses(w, r)
	case http.MethodPost:
		s.applyProcess(w, r, "")
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listProcesses(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	specs, err := s.mgr.ListSpecs(ctx)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out := ListProcessesResponse{Processes: make([]ProcessResponse, 0, len(specs))}
	for _, spec := range specs {
		insts, err := s.mgr.ListInstances(ctx, spec.ProcessID)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		pr := ProcessResponse{ProcessID: spec.ProcessID, Spec: specToDTO(spec)}
		for _, inst := range insts {
			pr.Instances = append(pr.Instances, Instance{
				InstanceID: inst.InstanceID,
				Ordinal:    inst.Ordinal,
				Desired:    string(inst.Desired),
				Observed:   string(inst.Observed),
				Health:     string(inst.Health),
				PID:        inst.PID,
			})
		}
		out.Processes = append(out.Processes, pr)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) processSub(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/processes/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	id := parts[0]
	rest := strings.Join(parts[1:], "/")
	if rest == "" {
		if r.Method == http.MethodPut {
			s.applyProcess(w, r, id)
			return
		}
		if r.Method == http.MethodGet {
			s.listOne(w, r, id)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch rest {
	case "start":
		s.mutateDesired(w, r, id, process.DesiredRunning)
	case "stop":
		s.mutateDesired(w, r, id, process.DesiredStopped)
	case "restart":
		s.restartHandler(w, r, id)
	case "reset-failure":
		s.resetFailureHandler(w, r, id)
	case "logs":
		s.logsHandler(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) listOne(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	spec, err := s.mgr.GetSpec(ctx, id)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	insts, err := s.mgr.ListInstances(ctx, id)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	pr := ProcessResponse{ProcessID: spec.ProcessID, Spec: specToDTO(spec)}
	for _, inst := range insts {
		pr.Instances = append(pr.Instances, Instance{
			InstanceID: inst.InstanceID,
			Ordinal:    inst.Ordinal,
			Desired:    string(inst.Desired),
			Observed:   string(inst.Observed),
			Health:     string(inst.Health),
			PID:        inst.PID,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ListProcessesResponse{Processes: []ProcessResponse{pr}})
}

func (s *Server) applyProcess(w http.ResponseWriter, r *http.Request, pathID string) {
	if !s.guardWrite(w, r) {
		return
	}
	var req CreateProcessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.requireOp(w, req.OperationID) {
		return
	}
	if s.replay(w, r, req.OperationID) {
		return
	}
	if pathID != "" && req.Spec.ProcessID == "" {
		req.Spec.ProcessID = pathID
	}
	if pathID != "" && req.Spec.ProcessID != pathID {
		http.Error(w, "process_id mismatch", http.StatusBadRequest)
		return
	}
	ps := dtoToSpec(req.Spec)
	got, err := s.mgr.ApplySpec(r.Context(), ps, req.ExpectedRevision, req.OperationID, req.Operator, "")
	if err != nil {
		s.writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "latest_revision": got.LatestRevision})
}

func (s *Server) mutateDesired(w http.ResponseWriter, r *http.Request, id string, desired process.DesiredState) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.guardWrite(w, r) {
		return
	}
	var req MutationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.requireOp(w, req.OperationID) {
		return
	}
	if s.replay(w, r, req.OperationID) {
		return
	}
	if err := s.mgr.SetDesired(r.Context(), id, desired, req.OperationID, req.Operator); err != nil {
		s.writeErr(w, err)
		return
	}
	if err := s.mgr.Reconcile(r.Context()); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (s *Server) restartHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.guardWrite(w, r) {
		return
	}
	var req MutationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.requireOp(w, req.OperationID) {
		return
	}
	if s.replay(w, r, req.OperationID) {
		return
	}
	if err := s.mgr.Restart(r.Context(), id, req.OperationID, req.Operator); err != nil {
		s.writeErr(w, err)
		return
	}
	_ = s.mgr.Reconcile(r.Context())
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (s *Server) resetFailureHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.guardWrite(w, r) {
		return
	}
	var req MutationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.requireOp(w, req.OperationID) {
		return
	}
	if s.replay(w, r, req.OperationID) {
		return
	}
	if err := s.mgr.ResetFailure(r.Context(), id, req.OperationID, req.Operator); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (s *Server) logsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/processes/")
	id = strings.TrimSuffix(id, "/logs")
	lines := 100
	if q := r.URL.Query().Get("lines"); q != "" {
		n, err := strconv.Atoi(q)
		if err == nil {
			lines = n
		}
	}
	insts, err := s.mgr.ListInstances(r.Context(), id)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	var all []string
	for _, inst := range insts {
		stdout, _ := logmgr.InstancePaths(s.mgr.Layout(), id, inst.InstanceID)
		got, err := logmgr.Tail(stdout, lines)
		if err != nil {
			continue
		}
		all = append(all, got...)
	}
	if all == nil {
		all = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(TailLogsResponse{Lines: all})
}

func (s *Server) instanceSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/instances/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "adopt" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.guardWrite(w, r) {
		return
	}
	var req AdoptInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.requireOp(w, req.OperationID) {
		return
	}
	if s.replay(w, r, req.OperationID) {
		return
	}
	if err := s.mgr.Adopt(r.Context(), parts[0], req.PID, req.OperationID, req.Operator); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (s *Server) guardWrite(w http.ResponseWriter, r *http.Request) bool {
	if s.isDegraded() {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "DEGRADED")
		return false
	}
	return true
}

func (s *Server) requireOp(w http.ResponseWriter, opID string) bool {
	if opID == "" {
		s.writeAPI(w, http.StatusBadRequest, "INVALID", "operation_id required")
		return false
	}
	return true
}

func (s *Server) replay(w http.ResponseWriter, r *http.Request, opID string) bool {
	status, result, errMsg, err := s.mgr.PeekOp(r.Context(), opID)
	if err != nil {
		if errcode.Is(err, errcode.NOT_FOUND) {
			return false
		}
		s.writeErr(w, err)
		return true
	}
	if status != "SUCCESS" && status != "FAILED" {
		return false
	}
	w.Header().Set(replayHeader, "1")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if len(result) > 0 {
		_, _ = w.Write(result)
		return true
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status, "error": errMsg})
	return true
}

func (s *Server) writeErr(w http.ResponseWriter, err error) {
	if errcode.Is(err, errcode.CONFLICT) {
		s.writeAPI(w, http.StatusConflict, "CONFLICT", err.Error())
		return
	}
	if errcode.Is(err, errcode.NOT_FOUND) {
		s.writeAPI(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	if errcode.Is(err, errcode.INVALID) {
		s.writeAPI(w, http.StatusBadRequest, "INVALID", err.Error())
		return
	}
	if errcode.Is(err, errcode.DEGRADED) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "DEGRADED")
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (s *Server) writeAPI(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{Code: code, Message: msg})
}

func specToDTO(s process.ProcessSpec) ProcessSpec {
	return ProcessSpec{
		ProcessID:        s.ProcessID,
		Name:             s.Name,
		OwnerAgentID:     s.OwnerAgentID,
		Group:            s.Group,
		Command:          s.Command,
		Args:             s.Args,
		WorkingDirectory: s.WorkingDirectory,
		RunAsUser:        s.RunAsUser,
		Environment:      s.Environment,
		Instances:        s.Instances,
		Autostart:        s.Autostart,
		StopSignal:       s.StopSignal,
		KillSignal:       s.KillSignal,
		StopTimeoutMs:    durationMS(s.StopTimeout),
		StartupPriority:  s.StartupPriority,
		Restart: RestartPolicyDTO{
			Mode:          string(s.Restart.Mode),
			MaxRetries:    s.Restart.MaxRetries,
			RetryWindowMs: durationMS(s.Restart.RetryWindow),
			Backoff: BackoffDTO{
				InitialMs:  durationMS(s.Restart.Backoff.Initial),
				MaxMs:      durationMS(s.Restart.Backoff.Max),
				Multiplier: s.Restart.Backoff.Multiplier,
			},
		},
		Health: HealthCheckDTO{
			Type:              s.Health.Type,
			URL:               s.Health.URL,
			Method:            s.Health.Method,
			Address:           s.Health.Address,
			Command:           s.Health.Command,
			ExpectedStatus:    s.Health.ExpectedStatus,
			Args:              s.Health.Args,
			InitialDelayMs:    durationMS(s.Health.InitialDelay),
			IntervalMs:        durationMS(s.Health.Interval),
			TimeoutMs:         durationMS(s.Health.Timeout),
			FailureThreshold:  s.Health.FailureThreshold,
			SuccessThreshold:  s.Health.SuccessThreshold,
			RestartOnFailure:  s.Health.RestartOnFailure,
			RestartCooldownMs: durationMS(s.Health.RestartCooldown),
		},
		Log: LogPolicyDTO{
			MaxSize:       s.Log.MaxSize,
			MaxFiles:      s.Log.MaxFiles,
			MaxAgeSeconds: durationSeconds(s.Log.MaxAge),
			Compress:      s.Log.Compress,
		},
		Resources: ResourceLimitDTO{
			CPUQuotaMillis: s.Resources.CPUQuotaMillis,
			MemoryBytes:    s.Resources.MemoryBytes,
			OpenFiles:      s.Resources.OpenFiles,
		},
		Dependencies:   depsToDTO(s.Dependencies),
		LatestRevision: s.LatestRevision,
	}
}

func dtoToSpec(s ProcessSpec) process.ProcessSpec {
	return process.ProcessSpec{
		ProcessID:        s.ProcessID,
		Name:             s.Name,
		OwnerAgentID:     s.OwnerAgentID,
		Group:            s.Group,
		Command:          s.Command,
		Args:             s.Args,
		WorkingDirectory: s.WorkingDirectory,
		RunAsUser:        s.RunAsUser,
		Environment:      s.Environment,
		Instances:        s.Instances,
		Autostart:        s.Autostart,
		StopSignal:       s.StopSignal,
		KillSignal:       s.KillSignal,
		StopTimeout:      fromMS(s.StopTimeoutMs),
		StartupPriority:  s.StartupPriority,
		Restart: process.RestartPolicy{
			Mode:        process.RestartMode(s.Restart.Mode),
			MaxRetries:  s.Restart.MaxRetries,
			RetryWindow: fromMS(s.Restart.RetryWindowMs),
			Backoff: process.Backoff{
				Initial:    fromMS(s.Restart.Backoff.InitialMs),
				Max:        fromMS(s.Restart.Backoff.MaxMs),
				Multiplier: s.Restart.Backoff.Multiplier,
			},
		},
		Health: process.HealthCheckSpec{
			Type:             s.Health.Type,
			URL:              s.Health.URL,
			Method:           s.Health.Method,
			Address:          s.Health.Address,
			Command:          s.Health.Command,
			ExpectedStatus:   s.Health.ExpectedStatus,
			Args:             s.Health.Args,
			InitialDelay:     fromMS(s.Health.InitialDelayMs),
			Interval:         fromMS(s.Health.IntervalMs),
			Timeout:          fromMS(s.Health.TimeoutMs),
			FailureThreshold: s.Health.FailureThreshold,
			SuccessThreshold: s.Health.SuccessThreshold,
			RestartOnFailure: s.Health.RestartOnFailure,
			RestartCooldown:  fromMS(s.Health.RestartCooldownMs),
		},
		Log: process.LogPolicy{
			MaxSize:  s.Log.MaxSize,
			MaxFiles: s.Log.MaxFiles,
			MaxAge:   fromSeconds(s.Log.MaxAgeSeconds),
			Compress: s.Log.Compress,
		},
		Resources: process.ResourceLimit{
			CPUQuotaMillis: s.Resources.CPUQuotaMillis,
			MemoryBytes:    s.Resources.MemoryBytes,
			OpenFiles:      s.Resources.OpenFiles,
		},
		Dependencies:   depsFromDTO(s.Dependencies),
		LatestRevision: s.LatestRevision,
	}
}

func depsToDTO(in []process.Dependency) []DependencyDTO {
	if in == nil {
		return nil
	}
	out := make([]DependencyDTO, len(in))
	for i, d := range in {
		out[i] = DependencyDTO{ProcessName: d.ProcessName, Condition: string(d.Condition)}
	}
	return out
}

func depsFromDTO(in []DependencyDTO) []process.Dependency {
	if in == nil {
		return nil
	}
	out := make([]process.Dependency, len(in))
	for i, d := range in {
		out[i] = process.Dependency{ProcessName: d.ProcessName, Condition: process.DepCondition(d.Condition)}
	}
	return out
}

func durationMS(d time.Duration) int64 { return d.Milliseconds() }

func fromMS(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

func durationSeconds(d time.Duration) int64 { return int64(d / time.Second) }

func fromSeconds(sec int64) time.Duration { return time.Duration(sec) * time.Second }
