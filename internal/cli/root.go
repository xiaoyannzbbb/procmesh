package cli

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultServer = "127.0.0.1:9000"

const usageText = `usage: procmesh [flags] <command>

flags:
  --server ADDR            Connect base (default 127.0.0.1:9000)
  --operation-id ID        mutation id (default generated UUID)
  --operator NAME          operator (default $USER or cli)
  --node NODE              target owner node_id or hostname
  --auth-token TOKEN       Bearer token (overrides session file)

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
  cluster init [--admin-user NAME]
  agent join --seed HOST:PORT --token TOKEN
  node list
  node status [id-or-hostname]
  node token create [--ttl DURATION] [--uses N]
  node token revoke TOKEN_ID
  node remove NODE_ID
  node promote NODE_ID
  login [--user NAME] [--password PASS]
  logout
  user list
  user create --user NAME --password PASS [--display NAME] [--email E]
  user disable USER_ID
  role list
  role create --name NAME --perm P [--perm P...]
  role grant --user-id ID --role-id ID [--scope CLUSTER|AGENT|AGENT_GROUP|PROCESS_GROUP] [--scope-id NODE]
  group list
  group create --name NAME [--description T]
  group delete GROUP_ID
  group add-member --group-id ID --node-id ID
  group remove-member --group-id ID --node-id ID
  batch create --type start|stop|restart|apply [--process-id ID]... [--process-name NODE:NAME]... [--agent-group-id ID] [--process-group NAME] [--file spec.yaml]
  batch get BATCH_ID
  batch list
  batch retry BATCH_ID
  batch replay-timeout BATCH_ID
  batch export BATCH_ID [--format json|csv]
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

	adminUser string
	seed      string
	token     string
	ttl       time.Duration
	uses      int32

	authToken string
	user      string
	password  string
	display   string
	email     string
	name        string
	perms       []string
	userID      string
	roleID      string
	scope       string
	scopeID     string
	description string
	groupID     string
	nodeID      string

	batchType     string
	processIDs    []string
	processNames  []string
	agentGroupID  string
	processGroup  string
	format        string

	args []string
}

// Main is the procmesh CLI entrypoint. args are os.Args[1:].
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opt, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprint(stderr, usageText)
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

	cmd, rest := opt.args[0], opt.args[1:]
	c := newClient(opt.server, opt.operationID, opt.operator, opt.node, sessionTokenFor(cmd, opt.server, opt.authToken))
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
	case "cluster":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing cluster subcommand"))
		}
		runErr = runCluster(c, rest[0], rest[1:], opt, stdout)
	case "agent":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing agent subcommand"))
		}
		runErr = runAgent(c, rest[0], rest[1:], opt, stdout)
	case "node":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing node subcommand"))
		}
		runErr = runNode(c, rest[0], rest[1:], opt, stdout)
	case "login":
		runErr = runLogin(c, opt, stdin, stdout)
	case "logout":
		if len(rest) != 0 {
			return printUsage(stderr, usageError("unexpected arguments"))
		}
		runErr = runLogout(c)
	case "user":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing user subcommand"))
		}
		runErr = runUser(c, rest[0], rest[1:], opt, stdout)
	case "role":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing role subcommand"))
		}
		runErr = runRole(c, rest[0], rest[1:], opt, stdout)
	case "group":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing group subcommand"))
		}
		runErr = runGroup(c, rest[0], rest[1:], opt, stdout)
	case "batch":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing batch subcommand"))
		}
		runErr = runBatch(c, rest[0], rest[1:], opt, stdout)
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
	opt := options{server: defaultServer, ttl: time.Hour, uses: 1}
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
	case "admin-user":
		opt.adminUser = val
	case "seed":
		opt.seed = val
	case "token":
		opt.token = val
	case "ttl":
		d, err := time.ParseDuration(val)
		if err != nil {
			return usageError("invalid --ttl")
		}
		opt.ttl = d
	case "uses":
		n, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			return usageError("invalid --uses")
		}
		opt.uses = int32(n)
	case "auth-token":
		opt.authToken = val
	case "user":
		opt.user = val
	case "password":
		opt.password = val
	case "display":
		opt.display = val
	case "email":
		opt.email = val
	case "name":
		opt.name = val
	case "perm":
		opt.perms = append(opt.perms, val)
	case "user-id":
		opt.userID = val
	case "role-id":
		opt.roleID = val
	case "scope":
		opt.scope = val
	case "scope-id":
		opt.scopeID = val
	case "description":
		opt.description = val
	case "group-id":
		opt.groupID = val
	case "node-id":
		opt.nodeID = val
	case "type":
		opt.batchType = val
	case "process-id":
		opt.processIDs = append(opt.processIDs, val)
	case "process-name":
		opt.processNames = append(opt.processNames, val)
	case "agent-group-id":
		opt.agentGroupID = val
	case "process-group":
		opt.processGroup = val
	case "format":
		opt.format = val
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
