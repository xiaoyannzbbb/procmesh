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

const defaultServer = "127.0.0.1:18680"

const usageText = `usage: procmesh [flags] <command>

flags:
  --server ADDR            Connect base (default 127.0.0.1:18680)
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
  metrics history node NODE_ID [--since RFC3339|unix] [--until RFC3339|unix]
  metrics history process <id-or-name> [--since RFC3339|unix] [--until RFC3339|unix]
  alert list [--state FIRING|RESOLVED]
  alert get ALERT_ID
  alert channel list
  alert channel put --type T --name N [--id ID] [--enabled true|false] [--config JSON]
  alert channel delete CHANNEL_ID
  alert policy get
  alert policy put --dedup-window-sec N --notify-on-resolve true|false --cpu N --memory N --disk N --consecutive N --suspect-too-long-sec N
  backup create --sink=fs|s3|peer [--process-id ID]... [--peer-node ID]...
  backup list [--sink S] [--peer-node ID]... [--include-s3]
  backup get SNAPSHOT_ID [--sink S] [--source-node ID] [--payload]
  backup delete SNAPSHOT_ID --sink S
  backup restore SNAPSHOT_ID --sink S --process-id ID --expected-revision N [--source-node ID]
`

type usageError string

func (e usageError) Error() string { return string(e) }
func (e usageError) isUsageError() {}

type usageErrorWithCause struct {
	message string
	cause   error
}

func (e usageErrorWithCause) Error() string { return fmt.Sprintf("%s: %v", e.message, e.cause) }
func (e usageErrorWithCause) Unwrap() error { return e.cause }
func (e usageErrorWithCause) isUsageError() {}

func isUsageError(err error) bool {
	_, ok := err.(interface{ isUsageError() })
	return ok
}

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

	authToken   string
	user        string
	password    string
	display     string
	email       string
	name        string
	perms       []string
	userID      string
	roleID      string
	scope       string
	scopeID     string
	description string
	groupID     string
	nodeID      string

	batchType    string
	processIDs   []string
	processNames []string
	agentGroupID string
	processGroup string
	format       string

	sinceUnix int64
	untilUnix int64

	state              string
	id                 string
	enabled            bool
	enabledSet         bool
	config             string
	dedupWindowSec     int64
	dedupWindowSet     bool
	notifyOnResolve    bool
	notifyOnResolveSet bool
	cpuHigh            int32
	cpuHighSet         bool
	memoryHigh         int32
	memoryHighSet      bool
	diskHigh           int32
	diskHighSet        bool
	consecutive        int32
	consecutiveSet     bool
	suspectTooLongSec  int64
	suspectTooLongSet  bool

	sink       string
	peerNodes  []string
	includeS3  bool
	sourceNode string
	payload    bool

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
	case "metrics":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing metrics subcommand"))
		}
		runErr = runMetrics(c, rest[0], rest[1:], opt, stdout)
	case "alert":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing alert subcommand"))
		}
		runErr = runAlert(c, rest[0], rest[1:], opt, stdout)
	case "backup":
		if len(rest) == 0 {
			return printUsage(stderr, usageError("missing backup subcommand"))
		}
		runErr = runBackup(c, rest[0], rest[1:], opt, stdout)
	default:
		return printUsage(stderr, usageError("unknown command"))
	}
	if runErr != nil {
		if isUsageError(runErr) {
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
			if isPresenceBoolFlag(name) {
				val = "true"
			} else {
				i++
				if i >= len(args) {
					return options{}, usageError("flag --" + name + " requires a value")
				}
				val = args[i]
			}
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
	case "since":
		n, err := parseUnixOrRFC3339(val)
		if err != nil {
			return usageError("invalid --since")
		}
		opt.sinceUnix = n
	case "until":
		n, err := parseUnixOrRFC3339(val)
		if err != nil {
			return usageError("invalid --until")
		}
		opt.untilUnix = n
	case "state":
		opt.state = val
	case "id":
		opt.id = val
	case "enabled":
		b, err := parseBoolFlag(val)
		if err != nil {
			return usageError("invalid --enabled")
		}
		opt.enabled = b
		opt.enabledSet = true
	case "config":
		opt.config = val
	case "dedup-window-sec":
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return usageError("invalid --dedup-window-sec")
		}
		opt.dedupWindowSec = n
		opt.dedupWindowSet = true
	case "notify-on-resolve":
		b, err := parseBoolFlag(val)
		if err != nil {
			return usageError("invalid --notify-on-resolve")
		}
		opt.notifyOnResolve = b
		opt.notifyOnResolveSet = true
	case "cpu":
		n, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			return usageError("invalid --cpu")
		}
		opt.cpuHigh = int32(n)
		opt.cpuHighSet = true
	case "memory":
		n, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			return usageError("invalid --memory")
		}
		opt.memoryHigh = int32(n)
		opt.memoryHighSet = true
	case "disk":
		n, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			return usageError("invalid --disk")
		}
		opt.diskHigh = int32(n)
		opt.diskHighSet = true
	case "consecutive":
		n, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			return usageError("invalid --consecutive")
		}
		opt.consecutive = int32(n)
		opt.consecutiveSet = true
	case "suspect-too-long-sec":
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return usageError("invalid --suspect-too-long-sec")
		}
		opt.suspectTooLongSec = n
		opt.suspectTooLongSet = true
	case "sink":
		opt.sink = val
	case "peer-node":
		opt.peerNodes = append(opt.peerNodes, val)
	case "include-s3":
		b, err := parseBoolFlag(val)
		if err != nil {
			return usageError("invalid --include-s3")
		}
		opt.includeS3 = b
	case "source-node":
		opt.sourceNode = val
	case "payload":
		b, err := parseBoolFlag(val)
		if err != nil {
			return usageError("invalid --payload")
		}
		opt.payload = b
	default:
		return usageError("unknown flag --" + name)
	}
	return nil
}

func isPresenceBoolFlag(name string) bool {
	switch name {
	case "payload", "include-s3":
		return true
	default:
		return false
	}
}

func parseBoolFlag(val string) (bool, error) {
	switch strings.ToLower(val) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool")
	}
}

func parseUnixOrRFC3339(val string) (int64, error) {
	if isAllDigits(val) {
		return strconv.ParseInt(val, 10, 64)
	}
	t, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
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
