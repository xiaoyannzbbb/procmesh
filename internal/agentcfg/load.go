package agentcfg

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"gopkg.in/yaml.v3"
)

type Config struct {
	DataDir    string
	Listen     string
	Advertise  string
	Pprof      Pprof
	Disk       logmgr.Policy
	Gossip     Gossip
	RPC        RPC
	Control    Control
	BreakGlass BreakGlass
	Batch      Batch
	Backup     Backup
	Process    Process
	Update     Update
}

// DefaultUpdateRepository is the GitHub owner/repo used by scripts/install.sh.
const DefaultUpdateRepository = "xiaoyannzbbb/procmesh"

type Update struct {
	Repository string
	Enabled    bool
}

func DefaultUpdate() Update {
	return Update{Repository: DefaultUpdateRepository, Enabled: true}
}

var updateRepositoryRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (u Update) Validate() error {
	if u.Repository == "" {
		return errcode.E(errcode.INVALID, "update repository required")
	}
	if !updateRepositoryRe.MatchString(u.Repository) {
		return errcode.E(errcode.INVALID, "update repository must be owner/repo")
	}
	return nil
}

type Process struct {
	DisableRemoteCreate bool
	DisableRemoteUpdate bool
	DisableRemoteDelete bool
}

type Backup struct {
	FSDir      string
	Schedule   string
	S3         S3
	S3Profiles map[string]S3
}

type S3 struct {
	Endpoint  string
	Bucket    string
	Prefix    string
	Region    string
	AccessKey string
	SecretKey string
	Insecure  bool
}

type Gossip struct {
	Listen      string
	Advertise   string
	Compression bool
}

type Pprof struct {
	Listen string
}

type RPC struct {
	Listen    string
	Advertise string
}

type Control struct {
	Listen    string
	Advertise string
}

type BreakGlass struct {
	Socket string
	Group  string
}

type Batch struct {
	MaxConcurrency int
	TargetTimeout  time.Duration
}

type file struct {
	DataDir    string          `yaml:"data_dir"`
	Listen     string          `yaml:"listen"`
	Advertise  string          `yaml:"advertise"`
	Pprof      *pprofFile      `yaml:"pprof"`
	Disk       *diskFile       `yaml:"disk"`
	Gossip     *gossipFile     `yaml:"gossip"`
	RPC        *rpcFile        `yaml:"rpc"`
	Control    *controlFile    `yaml:"control"`
	BreakGlass *breakGlassFile `yaml:"break_glass"`
	Batch      *batchFile      `yaml:"batch"`
	Backup     *backupFile     `yaml:"backup"`
	Process    *processFile    `yaml:"process"`
	Update     *updateFile     `yaml:"update"`
}

type pprofFile struct {
	Listen string `yaml:"listen"`
}

type diskFile struct {
	WarnPercent         *int  `yaml:"warn_percent"`
	CleanupPercent      *int  `yaml:"cleanup_percent"`
	EmergencyPercent    *int  `yaml:"emergency_percent"`
	AutoDelete          *bool `yaml:"auto_delete"`
	EmergencyStopWrites *bool `yaml:"emergency_stop_writes"`
}

type gossipFile struct {
	Listen      string `yaml:"listen"`
	Advertise   string `yaml:"advertise"`
	Compression bool   `yaml:"compression"`
}

type rpcFile struct {
	Listen    string `yaml:"listen"`
	Advertise string `yaml:"advertise"`
}

type controlFile struct {
	Listen    string `yaml:"listen"`
	Advertise string `yaml:"advertise"`
}

type breakGlassFile struct {
	Socket string `yaml:"socket"`
	Group  string `yaml:"group"`
}

type batchFile struct {
	MaxConcurrency *int   `yaml:"max_concurrency"`
	TargetTimeout  string `yaml:"target_timeout"`
}

type processFile struct {
	DisableRemoteCreate bool `yaml:"disable_remote_create"`
	DisableRemoteUpdate bool `yaml:"disable_remote_update"`
	DisableRemoteDelete bool `yaml:"disable_remote_delete"`
}

type updateFile struct {
	Repository string `yaml:"repository"`
	Enabled    *bool  `yaml:"enabled"`
}

type backupFile struct {
	FSDir      string            `yaml:"fs_dir"`
	Schedule   string            `yaml:"schedule"`
	S3         *s3File           `yaml:"s3"`
	S3Profiles map[string]s3File `yaml:"s3_profiles"`
}

type s3File struct {
	Endpoint  string `yaml:"endpoint"`
	Bucket    string `yaml:"bucket"`
	Prefix    string `yaml:"prefix"`
	Region    string `yaml:"region"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	Insecure  bool   `yaml:"insecure"`
}

func DefaultBatch() Batch {
	return Batch{
		MaxConcurrency: 16,
		TargetTimeout:  30 * time.Second,
	}
}

func (b Batch) Validate() error {
	if b.MaxConcurrency < 1 || b.MaxConcurrency > 64 {
		return errcode.E(errcode.INVALID, "batch max_concurrency must be 1-64")
	}
	if b.TargetTimeout <= 0 {
		return errcode.E(errcode.INVALID, "batch target_timeout must be >0")
	}
	return nil
}

func DefaultPath() string {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			// Unexpanded ~ so Load(..., false) treats it as missing.
			return "~/.config/procmesh/agent.yaml"
		}
		return filepath.Join(home, ".config/procmesh/agent.yaml")
	}
	return "/etc/procmesh/agent.yaml"
}

func Load(path string, required bool) (logmgr.Policy, error) {
	cfg, err := LoadAll(path, required)
	if err != nil {
		return logmgr.Policy{}, err
	}
	return cfg.Disk, nil
}

func LoadAll(path string, required bool) (Config, error) {
	if path == "" {
		if required {
			return Config{}, errcode.E(errcode.INVALID, "config file not found")
		}
		return applyS3Env(Config{Disk: logmgr.DefaultPolicy(), Batch: DefaultBatch(), Update: DefaultUpdate()}), nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if required {
				return Config{}, errcode.E(errcode.INVALID, "config file not found")
			}
			return applyS3Env(Config{Disk: logmgr.DefaultPolicy(), Batch: DefaultBatch(), Update: DefaultUpdate()}), nil
		}
		return Config{}, err
	}

	var f file
	if err := yaml.Unmarshal(b, &f); err != nil {
		return Config{}, err
	}

	p := logmgr.DefaultPolicy()
	if d := f.Disk; d != nil {
		if d.WarnPercent != nil {
			p.WarnPercent = *d.WarnPercent
		}
		if d.CleanupPercent != nil {
			p.CleanupPercent = *d.CleanupPercent
		}
		if d.EmergencyPercent != nil {
			p.EmergencyPercent = *d.EmergencyPercent
		}
		if d.AutoDelete != nil {
			p.AutoDelete = *d.AutoDelete
		}
		if d.EmergencyStopWrites != nil {
			p.EmergencyStopWrites = *d.EmergencyStopWrites
		}
	}
	if err := p.Validate(); err != nil {
		return Config{}, err
	}
	batch := DefaultBatch()
	if bf := f.Batch; bf != nil {
		if bf.MaxConcurrency != nil {
			batch.MaxConcurrency = *bf.MaxConcurrency
		}
		if bf.TargetTimeout != "" {
			d, err := time.ParseDuration(bf.TargetTimeout)
			if err != nil {
				return Config{}, errcode.E(errcode.INVALID, "batch target_timeout invalid")
			}
			batch.TargetTimeout = d
		}
	}
	if err := batch.Validate(); err != nil {
		return Config{}, err
	}
	upd := DefaultUpdate()
	if uf := f.Update; uf != nil {
		if uf.Repository != "" {
			upd.Repository = uf.Repository
		}
		if uf.Enabled != nil {
			upd.Enabled = *uf.Enabled
		}
	}
	if err := upd.Validate(); err != nil {
		return Config{}, err
	}
	cfg := Config{DataDir: f.DataDir, Listen: f.Listen, Advertise: f.Advertise, Disk: p, Batch: batch, Update: upd}
	if pprof := f.Pprof; pprof != nil {
		cfg.Pprof.Listen = pprof.Listen
	}
	if g := f.Gossip; g != nil {
		cfg.Gossip.Listen = g.Listen
		cfg.Gossip.Advertise = g.Advertise
		cfg.Gossip.Compression = g.Compression
	}
	if r := f.RPC; r != nil {
		cfg.RPC.Listen = r.Listen
		cfg.RPC.Advertise = r.Advertise
	}
	if c := f.Control; c != nil {
		cfg.Control.Listen = c.Listen
		cfg.Control.Advertise = c.Advertise
	}
	if bg := f.BreakGlass; bg != nil {
		cfg.BreakGlass.Socket = bg.Socket
		cfg.BreakGlass.Group = bg.Group
	}
	if pf := f.Process; pf != nil {
		cfg.Process = Process{
			DisableRemoteCreate: pf.DisableRemoteCreate,
			DisableRemoteUpdate: pf.DisableRemoteUpdate,
			DisableRemoteDelete: pf.DisableRemoteDelete,
		}
	}
	if bf := f.Backup; bf != nil {
		cfg.Backup.FSDir = bf.FSDir
		cfg.Backup.Schedule = bf.Schedule
		if s3 := bf.S3; s3 != nil {
			cfg.Backup.S3 = S3{
				Endpoint:  s3.Endpoint,
				Bucket:    s3.Bucket,
				Prefix:    s3.Prefix,
				Region:    s3.Region,
				AccessKey: s3.AccessKey,
				SecretKey: s3.SecretKey,
				Insecure:  s3.Insecure,
			}
		}
		if len(bf.S3Profiles) > 0 {
			cfg.Backup.S3Profiles = make(map[string]S3, len(bf.S3Profiles))
			for name, profile := range bf.S3Profiles {
				cfg.Backup.S3Profiles[name] = S3{
					Endpoint:  profile.Endpoint,
					Bucket:    profile.Bucket,
					Prefix:    profile.Prefix,
					Region:    profile.Region,
					AccessKey: profile.AccessKey,
					SecretKey: profile.SecretKey,
					Insecure:  profile.Insecure,
				}
			}
		}
	}
	return applyS3Env(cfg), nil
}

func applyS3Env(cfg Config) Config {
	if v := os.Getenv("PROCMESH_S3_ACCESS_KEY"); v != "" {
		cfg.Backup.S3.AccessKey = v
	}
	if v := os.Getenv("PROCMESH_S3_SECRET_KEY"); v != "" {
		cfg.Backup.S3.SecretKey = v
	}
	return cfg
}
