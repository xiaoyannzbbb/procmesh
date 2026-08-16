package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/qleelulu/procmesh/internal/auth"
	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"github.com/qleelulu/procmesh/internal/paths"
	"github.com/qleelulu/procmesh/internal/process"
	"github.com/qleelulu/procmesh/internal/rpc"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"github.com/qleelulu/procmesh/proto/procmesh/v1/procmeshv1connect"
)

const (
	MaxTailLines         = 10000
	DefaultTailLines     = 100
	MaxDownloadSize      = 50 << 20 // 50MiB
	MaxConcurrentStreams = 8
	StreamTimeout        = 5 * time.Minute

	downloadChunk = 32 * 1024
)

var _ procmeshv1connect.LogServiceHandler = (*LogAPI)(nil)

type LogAPI struct {
	Mgr       *process.Manager
	Auth      *auth.Service
	LocalOnly bool
	LocalID   string
	Router    *Router
	Forward   Forwarder

	mu      sync.Mutex
	streams int
}

func (s *LogAPI) hop(ctx context.Context, header http.Header, idOrName, ownerAgentID string) (local bool, rt Route, err error) {
	return hopRoute(s.LocalOnly, s.LocalID, s.Router, ctx, header, idOrName, ownerAgentID)
}

func (s *LogAPI) remoteLog(ctx context.Context, rt Route, header http.Header) (procmeshv1connect.LogServiceClient, error) {
	if s.Forward == nil {
		return nil, unavailableOwner()
	}
	stampHop(header, s.LocalID, rt.NodeID)
	stampIdentity(header, ctx)
	cli, err := s.Forward.Log(ctx, rt)
	if err != nil {
		return nil, ToConnect(rpc.MapDialError(err))
	}
	return cli, nil
}

func (s *LogAPI) TailLogs(ctx context.Context, req *connect.Request[procmeshv1.TailLogsRequest]) (*connect.Response[procmeshv1.TailLogsResponse], error) {
	local, rt, err := s.hop(ctx, req.Header(), req.Msg.GetIdOrName(), "")
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := authorizeProcessRoute(ctx, s.Auth, s.Router, auth.PermProcessLogsRead, req.Msg.GetIdOrName(), local, rt, false); err != nil {
		return nil, err
	}
	if !local {
		cli, err := s.remoteLog(ctx, rt, req.Header())
		if err != nil {
			return nil, err
		}
		out, err := cli.TailLogs(ctx, req)
		if err != nil {
			return nil, mapForwardErr(err)
		}
		return out, nil
	}
	if err := requireMgr(s.Mgr); err != nil {
		return nil, err
	}
	spec, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return nil, ToConnect(err)
	}
	if err := authorizeProcessSpec(ctx, s.Auth, auth.PermProcessLogsRead, s.LocalID, spec.Group, false); err != nil {
		return nil, err
	}
	stream, err := normalizeLogStream(req.Msg.GetStream())
	if err != nil {
		return nil, ToConnect(err)
	}
	lines := int(req.Msg.GetLines())
	if lines <= 0 {
		lines = DefaultTailLines
	}
	if lines > MaxTailLines {
		lines = MaxTailLines
	}
	insts, err := s.instanceIDs(ctx, spec.ProcessID, req.Msg.GetInstanceId(), false)
	if err != nil {
		return nil, ToConnect(err)
	}
	var all []string
	for _, instID := range insts {
		got, err := logmgr.Tail(logPath(s.Mgr.Layout(), spec.ProcessID, instID, stream), lines)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, ToConnect(err)
		}
		all = append(all, got...)
	}
	if all == nil {
		all = []string{}
	}
	return connect.NewResponse(&procmeshv1.TailLogsResponse{Lines: all}), nil
}

func (s *LogAPI) StreamLogs(ctx context.Context, req *connect.Request[procmeshv1.StreamLogsRequest], stream *connect.ServerStream[procmeshv1.LogChunk]) error {
	local, rt, err := s.hop(ctx, req.Header(), req.Msg.GetIdOrName(), "")
	if err != nil {
		return ToConnect(err)
	}
	if err := authorizeProcessRoute(ctx, s.Auth, s.Router, auth.PermProcessLogsRead, req.Msg.GetIdOrName(), local, rt, false); err != nil {
		return err
	}
	if !local {
		return s.forwardChunks(ctx, rt, req.Header(), stream, func(cli procmeshv1connect.LogServiceClient) (*connect.ServerStreamForClient[procmeshv1.LogChunk], error) {
			return cli.StreamLogs(ctx, req)
		})
	}
	if err := requireMgr(s.Mgr); err != nil {
		return err
	}
	spec, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return ToConnect(err)
	}
	if err := authorizeProcessSpec(ctx, s.Auth, auth.PermProcessLogsRead, s.LocalID, spec.Group, false); err != nil {
		return err
	}
	name, err := normalizeLogStream(req.Msg.GetStream())
	if err != nil {
		return ToConnect(err)
	}
	insts, err := s.instanceIDs(ctx, spec.ProcessID, req.Msg.GetInstanceId(), true)
	if err != nil {
		return ToConnect(err)
	}
	if err := s.acquireStream(); err != nil {
		return ToConnect(err)
	}
	defer s.releaseStream()

	ctx, cancel := context.WithTimeout(ctx, StreamTimeout)
	defer cancel()
	if len(insts) == 0 {
		return stream.Send(&procmeshv1.LogChunk{Eof: true})
	}
	path := logPath(s.Mgr.Layout(), spec.ProcessID, insts[0], name)
	ch, errCh := logmgr.Follow(ctx, path, true)
	for ch != nil || errCh != nil {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return stream.Send(&procmeshv1.LogChunk{Eof: true})
			}
			return nil
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err != nil {
				return ToConnect(err)
			}
		case chunk, ok := <-ch:
			if !ok {
				ch = nil
				continue
			}
			if err := stream.Send(&procmeshv1.LogChunk{Data: chunk}); err != nil {
				return err
			}
		}
	}
	return stream.Send(&procmeshv1.LogChunk{Eof: true})
}

func (s *LogAPI) DownloadLogs(ctx context.Context, req *connect.Request[procmeshv1.DownloadLogsRequest], stream *connect.ServerStream[procmeshv1.LogChunk]) error {
	local, rt, err := s.hop(ctx, req.Header(), req.Msg.GetIdOrName(), "")
	if err != nil {
		return ToConnect(err)
	}
	if err := authorizeProcessRoute(ctx, s.Auth, s.Router, auth.PermProcessLogsDownload, req.Msg.GetIdOrName(), local, rt, false); err != nil {
		return err
	}
	if !local {
		return s.forwardChunks(ctx, rt, req.Header(), stream, func(cli procmeshv1connect.LogServiceClient) (*connect.ServerStreamForClient[procmeshv1.LogChunk], error) {
			return cli.DownloadLogs(ctx, req)
		})
	}
	if err := requireMgr(s.Mgr); err != nil {
		return err
	}
	spec, err := s.Mgr.Resolve(ctx, req.Msg.GetIdOrName())
	if err != nil {
		return ToConnect(err)
	}
	if err := authorizeProcessSpec(ctx, s.Auth, auth.PermProcessLogsDownload, s.LocalID, spec.Group, false); err != nil {
		return err
	}
	name, err := normalizeLogStream(req.Msg.GetStream())
	if err != nil {
		return ToConnect(err)
	}
	insts, err := s.instanceIDs(ctx, spec.ProcessID, req.Msg.GetInstanceId(), true)
	if err != nil {
		return ToConnect(err)
	}
	if len(insts) == 0 {
		return stream.Send(&procmeshv1.LogChunk{Eof: true})
	}
	f, err := os.Open(logPath(s.Mgr.Layout(), spec.ProcessID, insts[0], name))
	if err != nil {
		if os.IsNotExist(err) {
			return stream.Send(&procmeshv1.LogChunk{Eof: true})
		}
		return ToConnect(err)
	}
	defer f.Close()

	buf := make([]byte, downloadChunk)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return ToConnect(err)
		}
		remain := int64(MaxDownloadSize) - total
		if remain <= 0 {
			return stream.Send(&procmeshv1.LogChunk{Eof: true})
		}
		toRead := buf
		if remain < int64(len(buf)) {
			toRead = buf[:remain]
		}
		n, err := f.Read(toRead)
		if n > 0 {
			total += int64(n)
		}
		last := err == io.EOF || total >= int64(MaxDownloadSize)
		if n > 0 || last {
			chunk := &procmeshv1.LogChunk{Eof: last}
			if n > 0 {
				chunk.Data = append([]byte(nil), toRead[:n]...)
			}
			if sendErr := stream.Send(chunk); sendErr != nil {
				return sendErr
			}
		}
		if last {
			return nil
		}
		if err != nil {
			return ToConnect(err)
		}
	}
}

func (s *LogAPI) forwardChunks(ctx context.Context, rt Route, header http.Header, out *connect.ServerStream[procmeshv1.LogChunk], call func(procmeshv1connect.LogServiceClient) (*connect.ServerStreamForClient[procmeshv1.LogChunk], error)) error {
	cli, err := s.remoteLog(ctx, rt, header)
	if err != nil {
		return err
	}
	in, err := call(cli)
	if err != nil {
		return mapForwardErr(err)
	}
	defer func() { _ = in.Close() }()
	for in.Receive() {
		if err := out.Send(in.Msg()); err != nil {
			return err
		}
	}
	return mapForwardErr(in.Err())
}

func (s *LogAPI) instanceIDs(ctx context.Context, processID, instanceID string, requireSingle bool) ([]string, error) {
	if instanceID != "" {
		inst, err := s.Mgr.GetInstance(ctx, instanceID)
		if err != nil {
			return nil, err
		}
		if inst.ProcessID != processID {
			return nil, errcode.E(errcode.NOT_FOUND, "instance")
		}
		return []string{instanceID}, nil
	}
	insts, err := s.Mgr.ListInstances(ctx, processID)
	if err != nil {
		return nil, err
	}
	if requireSingle && len(insts) > 1 {
		return nil, errcode.E(errcode.INVALID, "instance_id required")
	}
	ids := make([]string, 0, len(insts))
	for _, inst := range insts {
		ids = append(ids, inst.InstanceID)
	}
	return ids, nil
}

func (s *LogAPI) heldStreams() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams
}

func (s *LogAPI) acquireStream() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams >= MaxConcurrentStreams {
		return errcode.E(errcode.UNAVAILABLE, "too many log streams")
	}
	s.streams++
	return nil
}

func (s *LogAPI) releaseStream() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams > 0 {
		s.streams--
	}
}

func normalizeLogStream(name string) (string, error) {
	switch name {
	case "", "stdout":
		return "stdout", nil
	case "stderr":
		return "stderr", nil
	default:
		return "", errcode.E(errcode.INVALID, "stream must be stdout or stderr")
	}
}

func logPath(layout paths.Layout, processID, instanceID, stream string) string {
	stdout, stderr := logmgr.InstancePaths(layout, processID, instanceID)
	if stream == "stderr" {
		return stderr
	}
	return stdout
}
