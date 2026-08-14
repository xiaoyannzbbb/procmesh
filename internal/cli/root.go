package cli

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const defaultServer = "127.0.0.1:9000"

const usageText = `usage: procmesh [flags] <command>

flags:
  --server ADDR            Connect base (default 127.0.0.1:9000)
  --operation-id ID        mutation id (default generated UUID)
  --operator NAME          operator (default $USER or cli)
  --node NODE              remote owner (not supported until P3)

commands:
  status
  process list
  process get <id-or-name>
  process start <id-or-name>
  process stop <id-or-name>
  process restart <id-or-name>
  process kill <id-or-name>
  process logs <id-or-name> [--lines N] [--instance ID] [--stream stdout|stderr]
  process apply --file spec.yaml --expected-revision N [--comment T]
  process delete <id-or-name> --expected-revision N
  process history <id-or-name>
  process rollback <id-or-name> --to N --expected-revision M
  process reset-failure <id-or-name>
  process adopt <instance-id> --pid N
  start|stop|restart|logs <id-or-name>
`

type usageError string

func (e usageError) Error() string { return string(e) }

type options struct {
	server      string
	operationID string
	operator    string
	node        string

	file             string
	expectedRevision int64
	expectedSet      bool
	comment          string
	lines            int32
	instance         string
	stream           string
	toRevision       int64
	toSet            bool
	pid              int32
	pidSet           bool

	args []string
}

// Main is the procmesh CLI entrypoint. args are os.Args[1:].
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin
	opt, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprint(stderr, usageText)
		return 2
	}
	if opt.node != "" {
		fmt.Fprintln(stderr, "remote --node is not supported until P3")
		return 2
	}
	if len(opt.args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	if opt.operationID == "" {
		id, err := newUUID()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		opt.operationID = id
	}
	if opt.operator == "" {
		opt.operator = defaultOperator()
	}

	c := newClient(opt.server, opt.operationID, opt.operator)
	cmd, rest := opt.args[0], opt.args[1:]
	var runErr error
	switch cmd {
	case "status":
		if len(rest) != 0 {
			return printUsage(stderr, usageError("unexpected arguments"))
		}
		runErr = runStatus(c, stdout)
	case "process":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing process subcommand"))
		}
		runErr = runProcess(c, rest[0], rest[1:], opt, stdout)
	case "start", "stop", "restart", "logs":
		runErr = runProcess(c, cmd, rest, opt, stdout)
	default:
		return printUsage(stderr, usageError("unknown command"))
	}
	if runErr != nil {
		if _, ok := runErr.(usageError); ok {
			return printUsage(stderr, runErr)
		}
		fmt.Fprintln(stderr, formatErr(runErr))
		return 1
	}
	return 0
}

func printUsage(stderr io.Writer, err error) int {
	if err != nil && err.Error() != "" {
		fmt.Fprintln(stderr, err)
	}
	fmt.Fprint(stderr, usageText)
	return 2
}

func parseArgs(args []string) (options, error) {
	opt := options{server: defaultServer}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasVal, isFlag := splitFlag(a)
		if !isFlag {
			opt.args = append(opt.args, a)
			continue
		}
		if name == "" {
			return options{}, usageError("unknown flag")
		}
		if !hasVal {
			i++
			if i >= len(args) {
				return options{}, usageError("flag --" + name + " requires a value")
			}
			val = args[i]
		}
		if err := applyFlag(&opt, name, val); err != nil {
			return options{}, err
		}
	}
	return opt, nil
}

func splitFlag(a string) (name, value string, hasValue, ok bool) {
	if !strings.HasPrefix(a, "--") {
		return "", "", false, false
	}
	body := strings.TrimPrefix(a, "--")
	if body == "" {
		return "", "", false, true
	}
	if i := strings.IndexByte(body, '='); i >= 0 {
		return body[:i], body[i+1:], true, true
	}
	return body, "", false, true
}

func applyFlag(opt *options, name, val string) error {
	switch name {
	case "server":
		opt.server = val
	case "operation-id":
		opt.operationID = val
	case "operator":
		opt.operator = val
	case "node":
		opt.node = val
	case "file":
		opt.file = val
	case "expected-revision":
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return usageError("invalid --expected-revision")
		}
		opt.expectedRevision = n
		opt.expectedSet = true
	case "comment":
		opt.comment = val
	case "lines":
		n, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			return usageError("invalid --lines")
		}
		opt.lines = int32(n)
	case "instance":
		opt.instance = val
	case "stream":
		opt.stream = val
	case "to":
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return usageError("invalid --to")
		}
		opt.toRevision = n
		opt.toSet = true
	case "pid":
		n, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			return usageError("invalid --pid")
		}
		opt.pid = int32(n)
		opt.pidSet = true
	default:
		return usageError("unknown flag --" + name)
	}
	return nil
}

func defaultOperator() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "cli"
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
