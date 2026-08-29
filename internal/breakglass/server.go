package breakglass

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/api"
	"github.com/qleelulu/procmesh/internal/control"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	"github.com/qleelulu/procmesh/internal/store"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

const (
	ownerSocketMode         = 0o600
	groupSocketMode         = 0o660
	portableSocketPathLimit = 90
	auditTimeout            = 2 * time.Second
)

type Peer struct {
	PID      int
	UID      int
	GID      int
	Username string
	Groups   []string
}

type Config struct {
	SocketPath string
	Group      string
	LocalID    string
	Manager    *process.Manager
	Audit      AuditStore
	Recovery   func() RecoveryStore
	PeerLookup func(net.Conn) (Peer, error)
}

type AuditStore interface {
	AppendAudit(context.Context, store.AuditEvent) error
}

type RecoveryStore interface {
	View() control.State
	Apply(control.Command, time.Duration) error
}

type Server struct {
	http       *http.Server
	listener   net.Listener
	socketPath string
}

func DefaultSocketPath(dataDir string) string {
	candidate := filepath.Join(dataDir, "break-glass.sock")
	if len(candidate) <= portableSocketPathLimit {
		return candidate
	}
	digest := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return filepath.Join(os.TempDir(), fmt.Sprintf("procmesh-bg-%x.sock", digest[:8]))
}

func New(cfg Config) (*Server, error) {
	if cfg.SocketPath == "" {
		return nil, fmt.Errorf("break-glass socket path required")
	}
	if cfg.LocalID == "" || cfg.Manager == nil || cfg.Audit == nil {
		return nil, fmt.Errorf("break-glass local identity, process manager, and audit store required")
	}
	allowedGID, err := resolveGroup(cfg.Group)
	if err != nil {
		return nil, err
	}
	if err := prepareSocketPath(cfg.SocketPath); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("listen break-glass socket: %w", err)
	}
	mode := os.FileMode(ownerSocketMode)
	if allowedGID >= 0 {
		if err := os.Chown(cfg.SocketPath, -1, allowedGID); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("set break-glass socket group: %w", err)
		}
		mode = groupSocketMode
	}
	if err := os.Chmod(cfg.SocketPath, mode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("set break-glass socket mode: %w", err)
	}

	lookup := cfg.PeerLookup
	if lookup == nil {
		lookup = lookupPeer
	}
	authorizer := &accessController{
		audit:      cfg.Audit,
		manager:    cfg.Manager,
		localID:    cfg.LocalID,
		serverUID:  os.Geteuid(),
		allowedGID: allowedGID,
		socketPath: cfg.SocketPath,
	}
	owner := localOwner{manager: cfg.Manager, localID: cfg.LocalID}
	processAPI := &localProcessAPI{
		ProcessAPI: &api.ProcessAPI{Mgr: cfg.Manager, LocalOnly: true, LocalID: cfg.LocalID},
		owner:      owner,
	}
	logAPI := &localLogAPI{
		LogAPI: &api.LogAPI{Mgr: cfg.Manager, LocalOnly: true, LocalID: cfg.LocalID},
		owner:  owner,
	}
	intercept := connect.WithInterceptors(authorizer)
	processPath, processHandler := procmeshv1connect.NewProcessServiceHandler(processAPI, intercept)
	logPath, logHandler := procmeshv1connect.NewLogServiceHandler(logAPI, intercept)
	userPath, userHandler := procmeshv1connect.NewUserServiceHandler(&recoveryUserAPI{store: cfg.Recovery}, intercept)
	mux := http.NewServeMux()
	mux.Handle(processPath, processHandler)
	mux.Handle(logPath, logHandler)
	mux.Handle(userPath, userHandler)
	mux.HandleFunc("/", authorizer.rejectUnknown)
	httpServer := &http.Server{
		Handler: mux,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			peer := Peer{PID: -1, UID: -1, GID: -1, Username: "unknown"}
			lookedUp, err := lookup(conn)
			if err == nil {
				peer = enrichPeer(lookedUp)
			}
			return context.WithValue(ctx, peerKey{}, peerResult{peer: peer, err: err})
		},
	}
	return &Server{http: httpServer, listener: ln, socketPath: cfg.SocketPath}, nil
}

func (s *Server) Serve() error {
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve break-glass socket: %w", err)
}

func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.http.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown break-glass server: %w", err)
	}
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove break-glass socket: %w", err)
	}
	return nil
}

func prepareSocketPath(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create break-glass socket directory: %w", err)
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect break-glass socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("break-glass socket path exists and is not a socket")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale break-glass socket: %w", err)
	}
	return nil
}

func resolveGroup(name string) (int, error) {
	if name == "" {
		return -1, nil
	}
	group, err := user.LookupGroup(name)
	if err != nil {
		return -1, fmt.Errorf("look up break-glass group %q: %w", name, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return -1, fmt.Errorf("parse break-glass group %q gid: %w", name, err)
	}
	return gid, nil
}

type peerKey struct{}

type peerResult struct {
	peer Peer
	err  error
}

func enrichPeer(peer Peer) Peer {
	account, err := user.LookupId(strconv.Itoa(peer.UID))
	if err != nil {
		return peer
	}
	peer.Username = account.Username
	peer.Groups, _ = account.GroupIds()
	return peer
}

type accessController struct {
	audit      AuditStore
	manager    *process.Manager
	localID    string
	serverUID  int
	allowedGID int
	socketPath string
}

func (a *accessController) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		description := describeRequest(req.Spec().Procedure, req.Any())
		description.reason = strings.TrimSpace(req.Header().Get(rpc.HeaderBreakGlassReason))
		a.describeProcess(ctx, &description)
		peer, authorized := a.authorize(ctx)
		if !authorized {
			if err := a.record(peer, description, "denied", "DENIED"); err != nil {
				return nil, auditUnavailable(err)
			}
			return nil, accessDenied()
		}
		if targetNode := strings.TrimSpace(rpc.TargetOf(req.Header())); targetNode != "" && targetNode != a.localID {
			if err := a.record(peer, description, "denied", "DENIED"); err != nil {
				return nil, auditUnavailable(err)
			}
			return nil, accessDenied()
		}
		if !description.allowed {
			if err := a.record(peer, description, "denied", "DENIED"); err != nil {
				return nil, auditUnavailable(err)
			}
			return nil, accessDenied()
		}
		if description.mutation {
			if strings.TrimSpace(description.operationID) == "" {
				if err := a.record(peer, description, "error", "INVALID"); err != nil {
					return nil, auditUnavailable(err)
				}
				return nil, api.ToConnect(errcode.E(errcode.INVALID, "operation_id required"))
			}
			if description.reason == "" {
				if err := a.record(peer, description, "error", "INVALID"); err != nil {
					return nil, auditUnavailable(err)
				}
				return nil, api.ToConnect(errcode.E(errcode.INVALID, "break-glass reason required"))
			}
			switch message := req.Any().(type) {
			case *procmeshv1.ProcessRefRequest:
				message.Meta.OperationId = strings.TrimSpace(message.Meta.GetOperationId())
				message.Meta.Operator = peerOperator(peer)
				description.operationID = message.Meta.GetOperationId()
			case *procmeshv1.EnableUserRequest:
				if message.Meta == nil {
					message.Meta = &procmeshv1.MutationMeta{}
				}
				message.Meta.OperationId = strings.TrimSpace(message.Meta.GetOperationId())
				message.Meta.Operator = peerOperator(peer)
				description.operationID = message.Meta.GetOperationId()
			}
		}
		response, callErr := next(ctx, req)
		result := "success"
		if callErr != nil {
			result = "error"
		}
		if err := a.record(peer, description, result, redactedErrorCode(callErr)); err != nil {
			return nil, auditUnavailable(err)
		}
		return response, callErr
	}
}

func (a *accessController) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *accessController) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		peer, _ := a.authorize(ctx)
		description := requestDescription{action: "break_glass.reject", target: conn.Spec().Procedure}
		if err := a.record(peer, description, "denied", "DENIED"); err != nil {
			return auditUnavailable(err)
		}
		return accessDenied()
	}
}

func (a *accessController) authorize(ctx context.Context) (Peer, bool) {
	result, ok := ctx.Value(peerKey{}).(peerResult)
	if !ok || result.err != nil {
		return result.peer, false
	}
	peer := result.peer
	if peer.UID == 0 || peer.UID == a.serverUID {
		return peer, true
	}
	if a.allowedGID < 0 {
		return peer, false
	}
	want := strconv.Itoa(a.allowedGID)
	if peer.GID == a.allowedGID {
		return peer, true
	}
	for _, gid := range peer.Groups {
		if gid == want {
			return peer, true
		}
	}
	return peer, false
}

func (a *accessController) rejectUnknown(w http.ResponseWriter, r *http.Request) {
	peer, _ := a.authorize(r.Context())
	description := requestDescription{action: "break_glass.reject", target: r.URL.Path}
	if err := a.record(peer, description, "denied", "DENIED"); err != nil {
		http.Error(w, "break-glass audit unavailable", http.StatusServiceUnavailable)
		return
	}
	http.Error(w, "break-glass access denied", http.StatusForbidden)
}

func (a *accessController) describeProcess(ctx context.Context, description *requestDescription) {
	if a.manager == nil || !strings.HasPrefix(description.action, "break_glass.process.") || description.target == "" || description.target == "local-agent" {
		return
	}
	spec, err := a.manager.Resolve(ctx, description.target)
	if err != nil {
		return
	}
	description.processID = spec.ProcessID
	description.processName = spec.Name
}

func (a *accessController) record(peer Peer, description requestDescription, result, errorCode string) error {
	osUser := peerOperator(peer)
	metadata, _ := json.Marshal(map[string]any{
		"os_uid":       peer.UID,
		"os_gid":       peer.GID,
		"os_pid":       peer.PID,
		"os_user":      osUser,
		"socket":       a.socketPath,
		"target":       description.target,
		"process_id":   description.processID,
		"process_name": description.processName,
		"reason":       description.reason,
		"error_code":   errorCode,
	})
	resource := description.target
	if description.processID != "" {
		resource = description.processID
	}
	ctx, cancel := context.WithTimeout(context.Background(), auditTimeout)
	defer cancel()
	if err := a.audit.AppendAudit(ctx, store.AuditEvent{
		Timestamp:   time.Now().UTC(),
		UserID:      "uid:" + strconv.Itoa(peer.UID),
		Username:    osUser,
		SourceIP:    "unix",
		TargetAgent: a.localID,
		Resource:    resource,
		Action:      description.action,
		OperationID: description.operationID,
		Result:      result,
		Metadata:    metadata,
	}); err != nil {
		return fmt.Errorf("append break-glass audit: %w", err)
	}
	return nil
}

func peerOperator(peer Peer) string {
	if peer.Username != "" {
		return peer.Username
	}
	return "uid:" + strconv.Itoa(peer.UID)
}

func redactedErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		for _, detail := range connectErr.Details() {
			message, detailErr := detail.Value()
			if detailErr != nil {
				continue
			}
			if info, ok := message.(*procmeshv1.ErrorInfo); ok && info.GetCode() != "" {
				return info.GetCode()
			}
		}
	}
	return strings.ToUpper(connect.CodeOf(err).String())
}

func accessDenied() error {
	return api.ToConnect(errcode.E(errcode.DENIED, "break-glass access denied"))
}

func auditUnavailable(err error) error {
	return api.ToConnect(errcode.Wrap(errcode.UNAVAILABLE, "break-glass audit unavailable", err))
}

type requestDescription struct {
	action      string
	target      string
	allowed     bool
	mutation    bool
	operationID string
	reason      string
	processID   string
	processName string
}

func describeRequest(procedure string, message any) requestDescription {
	switch procedure {
	case procmeshv1connect.ProcessServiceListProcessesProcedure:
		return requestDescription{action: "break_glass.process.list", target: "local-agent", allowed: true}
	case procmeshv1connect.ProcessServiceGetProcessProcedure:
		request, _ := message.(*procmeshv1.GetProcessRequest)
		return requestDescription{action: "break_glass.process.get", target: request.GetIdOrName(), allowed: true}
	case procmeshv1connect.LogServiceTailLogsProcedure:
		request, _ := message.(*procmeshv1.TailLogsRequest)
		return requestDescription{action: "break_glass.process.logs", target: request.GetIdOrName(), allowed: true}
	case procmeshv1connect.ProcessServiceStartProcessProcedure,
		procmeshv1connect.ProcessServiceStopProcessProcedure,
		procmeshv1connect.ProcessServiceRestartProcessProcedure,
		procmeshv1connect.ProcessServiceKillProcessProcedure:
		request, _ := message.(*procmeshv1.ProcessRefRequest)
		action := ""
		switch procedure {
		case procmeshv1connect.ProcessServiceStartProcessProcedure:
			action = "start"
		case procmeshv1connect.ProcessServiceStopProcessProcedure:
			action = "stop"
		case procmeshv1connect.ProcessServiceRestartProcessProcedure:
			action = "restart"
		case procmeshv1connect.ProcessServiceKillProcessProcedure:
			action = "kill"
		}
		return requestDescription{
			action:      "break_glass.process." + action,
			target:      request.GetIdOrName(),
			allowed:     true,
			mutation:    true,
			operationID: request.GetMeta().GetOperationId(),
		}
	case procmeshv1connect.UserServiceEnableUserProcedure:
		request, _ := message.(*procmeshv1.EnableUserRequest)
		return requestDescription{
			action:      "break_glass.user.enable",
			target:      request.GetUserId(),
			allowed:     true,
			mutation:    true,
			operationID: request.GetMeta().GetOperationId(),
		}
	default:
		if request, ok := message.(*procmeshv1.ProcessRefRequest); ok && request.GetIdOrName() != "" {
			return requestDescription{action: "break_glass.reject", target: request.GetIdOrName()}
		}
		name := strings.TrimPrefix(procedure, "/")
		return requestDescription{action: "break_glass.reject", target: name}
	}
}

type recoveryUserAPI struct {
	procmeshv1connect.UnimplementedUserServiceHandler
	store func() RecoveryStore
}

func (s *recoveryUserAPI) EnableUser(_ context.Context, req *connect.Request[procmeshv1.EnableUserRequest]) (*connect.Response[procmeshv1.EnableUserResponse], error) {
	if s.store == nil {
		return nil, api.ToConnect(errcode.E(errcode.UNAVAILABLE, "control recovery unavailable"))
	}
	store := s.store()
	if store == nil {
		return nil, api.ToConnect(errcode.E(errcode.UNAVAILABLE, "control recovery unavailable"))
	}
	userID := strings.TrimSpace(req.Msg.GetUserId())
	if userID == "" {
		return nil, api.ToConnect(errcode.E(errcode.INVALID, "user_id required"))
	}
	cmd, err := control.EncodeCommand(control.CmdUserEnable, control.UserEnableBody{UserID: userID})
	if err != nil {
		return nil, api.ToConnect(err)
	}
	if err := store.Apply(cmd, 5*time.Second); err != nil {
		return nil, api.ToConnect(err)
	}
	state := store.View()
	username, ok := state.UsersByID[userID]
	if !ok {
		return nil, api.ToConnect(errcode.E(errcode.NOT_FOUND, "user not found"))
	}
	user, ok := state.Users[username]
	if !ok {
		return nil, api.ToConnect(errcode.E(errcode.NOT_FOUND, "user not found"))
	}
	return connect.NewResponse(&procmeshv1.EnableUserResponse{User: &procmeshv1.User{
		UserId: user.ID, Username: user.Username, DisplayName: user.DisplayName, Email: user.Email,
		Status: string(user.Status), CreatedUnix: user.CreatedUnix, LastLoginUnix: user.LastLoginUnix,
	}}), nil
}

type localProcessAPI struct {
	*api.ProcessAPI
	owner localOwner
}

func (s *localProcessAPI) ListProcesses(ctx context.Context, req *connect.Request[procmeshv1.ListProcessesRequest]) (*connect.Response[procmeshv1.ListProcessesResponse], error) {
	response, err := s.ProcessAPI.ListProcesses(ctx, req)
	if err != nil {
		return nil, err
	}
	local := response.Msg.Processes[:0]
	for _, view := range response.Msg.Processes {
		owner := view.GetSpec().GetOwnerAgentId()
		if s.owner.matches(owner) {
			local = append(local, view)
		}
	}
	response.Msg.Processes = local
	return response, nil
}

func (s *localProcessAPI) GetProcess(ctx context.Context, req *connect.Request[procmeshv1.GetProcessRequest]) (*connect.Response[procmeshv1.GetProcessResponse], error) {
	if err := s.owner.require(ctx, req.Msg.GetIdOrName()); err != nil {
		return nil, err
	}
	return s.ProcessAPI.GetProcess(ctx, req)
}

func (s *localProcessAPI) StartProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	if err := s.owner.require(ctx, req.Msg.GetIdOrName()); err != nil {
		return nil, err
	}
	return s.ProcessAPI.StartProcess(ctx, req)
}

func (s *localProcessAPI) StopProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	if err := s.owner.require(ctx, req.Msg.GetIdOrName()); err != nil {
		return nil, err
	}
	return s.ProcessAPI.StopProcess(ctx, req)
}

func (s *localProcessAPI) RestartProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	if err := s.owner.require(ctx, req.Msg.GetIdOrName()); err != nil {
		return nil, err
	}
	return s.ProcessAPI.RestartProcess(ctx, req)
}

func (s *localProcessAPI) KillProcess(ctx context.Context, req *connect.Request[procmeshv1.ProcessRefRequest]) (*connect.Response[procmeshv1.ProcessRefResponse], error) {
	if err := s.owner.require(ctx, req.Msg.GetIdOrName()); err != nil {
		return nil, err
	}
	return s.ProcessAPI.KillProcess(ctx, req)
}

type localOwner struct {
	manager *process.Manager
	localID string
}

func (o localOwner) matches(ownerID string) bool {
	return ownerID == o.localID
}

func (o localOwner) require(ctx context.Context, idOrName string) error {
	spec, err := o.manager.Resolve(ctx, idOrName)
	if err != nil {
		return api.ToConnect(fmt.Errorf("resolve local process owner: %w", err))
	}
	if !o.matches(spec.OwnerAgentID) {
		return api.ToConnect(errcode.E(errcode.NOT_FOUND, "process"))
	}
	return nil
}

type localLogAPI struct {
	*api.LogAPI
	owner localOwner
}

func (s *localLogAPI) TailLogs(ctx context.Context, req *connect.Request[procmeshv1.TailLogsRequest]) (*connect.Response[procmeshv1.TailLogsResponse], error) {
	if err := s.owner.require(ctx, req.Msg.GetIdOrName()); err != nil {
		return nil, err
	}
	return s.LogAPI.TailLogs(ctx, req)
}
