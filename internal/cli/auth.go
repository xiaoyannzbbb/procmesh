package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
	"golang.org/x/term"
)

type fileSession struct {
	Server      string `json:"server"`
	SessionID   string `json:"session_id"`
	UserID      string `json:"user_id"`
	ExpiresUnix int64  `json:"expires_unix"`
}

var sessionFileFn = defaultSessionPath

var (
	isTerminalFn   = term.IsTerminal
	readPasswordFn = term.ReadPassword
)

func sessionPath() string { return sessionFileFn() }

func defaultSessionPath() string {
	if p := os.Getenv("PROCMESH_SESSION"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join("procmesh", "session")
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "procmesh", "session")
}

func writeSession(path string, s fileSession) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func readSession(path string) (fileSession, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fileSession{}, err
	}
	var s fileSession
	if err := json.Unmarshal(raw, &s); err != nil {
		return fileSession{}, err
	}
	return s, nil
}

func runLogin(c *client, opt options, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(opt.args) != 1 {
		return usageError("unexpected arguments")
	}
	user := opt.user
	if user == "" {
		user = "admin"
	}
	pass, err := resolvePassword(opt.password, stdin, stderr)
	if err != nil {
		return err
	}
	var ttlSeconds int64
	if opt.ttlSet {
		if opt.ttl < time.Second {
			return usageError("invalid --ttl")
		}
		ttlSeconds = int64(opt.ttl / time.Second)
	}
	resp, err := c.auth.Login(context.Background(), connect.NewRequest(&procmeshv1.LoginRequest{
		Username:   user,
		Password:   pass,
		TtlSeconds: ttlSeconds,
	}))
	if err != nil {
		return err
	}
	if err := writeSession(sessionPath(), fileSession{
		Server:      c.base,
		SessionID:   resp.Msg.GetSessionId(),
		UserID:      resp.Msg.GetUserId(),
		ExpiresUnix: resp.Msg.GetExpiresUnix(),
	}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "user_id=%s\n", resp.Msg.GetUserId())
	fmt.Fprintf(stdout, "username=%s\n", resp.Msg.GetUsername())
	fmt.Fprintf(stdout, "expires_unix=%d\n", resp.Msg.GetExpiresUnix())
	return nil
}

func runLogout(c *client) error {
	_, err := c.auth.Logout(context.Background(), connect.NewRequest(&procmeshv1.LogoutRequest{
		Meta: c.meta(),
	}))
	_ = os.Remove(sessionPath())
	return err
}

func resolvePassword(flag string, stdin io.Reader, stderr io.Writer) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if p := os.Getenv("PROCMESH_PASSWORD"); p != "" {
		return p, nil
	}
	if stdin == nil {
		return "", usageError("login requires a password")
	}
	if input, ok := stdin.(*os.File); ok && isTerminalFn(int(input.Fd())) {
		if stderr != nil {
			fmt.Fprint(stderr, "Password: ")
		}
		password, err := readPasswordFn(int(input.Fd()))
		if stderr != nil {
			fmt.Fprintln(stderr)
		}
		if err != nil {
			return "", usageErrorWithCause{message: "login could not read password", cause: err}
		}
		return requirePassword(string(password))
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !(err == io.EOF && line != "") {
		return "", usageError("login requires a password")
	}
	return requirePassword(line)
}

func requirePassword(password string) (string, error) {
	password = strings.TrimRight(password, "\r\n")
	if password == "" {
		return "", usageError("login requires a password")
	}
	return password, nil
}

func sameServer(a, b string) bool {
	return normalizeServer(a) == normalizeServer(b)
}

func sessionTokenFor(cmd, server, flagToken string) string {
	if cmd == "login" || flagToken != "" {
		return flagToken
	}
	sess, err := readSession(sessionPath())
	if err != nil {
		return ""
	}
	if os.Getenv("PROCMESH_SESSION") == "" && !sameServer(sess.Server, server) {
		return ""
	}
	return sess.SessionID
}
