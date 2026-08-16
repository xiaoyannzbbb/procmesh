package agentcfg

import (
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Disk    logmgr.Policy
	Gossip  Gossip
	RPC     RPC
	Control Control
	Batch   Batch
}

type Gossip struct {
	Listen    string
	Advertise string
}

type RPC struct {
	Listen    string
	Advertise string
}

type Control struct {
	Listen    string
	Advertise string
}

type Batch struct {
	MaxConcurrency int
	TargetTimeout  time.Duration
}

type file struct {
	Disk    *diskFile    `yaml:"disk"`
	Gossip  *gossipFile  `yaml:"gossip"`
	RPC     *rpcFile     `yaml:"rpc"`
	Control *controlFile `yaml:"control"`
	Batch   *batchFile   `yaml:"batch"`
}

type diskFile struct {
	WarnPercent         *int  `yaml:"warn_percent"`
	CleanupPercent      *int  `yaml:"cleanup_percent"`
	EmergencyPercent    *int  `yaml:"emergency_percent"`
	AutoDelete          *bool `yaml:"auto_delete"`
	EmergencyStopWrites *bool `yaml:"emergency_stop_writes"`
}

type gossipFile struct {
	Listen    string `yaml:"listen"`
	Advertise string `yaml:"advertise"`
}

type rpcFile struct {
	Listen    string `yaml:"listen"`
	Advertise string `yaml:"advertise"`
}

type controlFile struct {
	Listen    string `yaml:"listen"`
	Advertise string `yaml:"advertise"`
}

type batchFile struct {
	MaxConcurrency *int   `yaml:"max_concurrency"`
	TargetTimeout  string `yaml:"target_timeout"`
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
		return Config{Disk: logmgr.DefaultPolicy(), Batch: DefaultBatch()}, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if required {
				return Config{}, errcode.E(errcode.INVALID, "config file not found")
			}
			return Config{Disk: logmgr.DefaultPolicy(), Batch: DefaultBatch()}, nil
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
	cfg := Config{Disk: p, Batch: batch}
	if g := f.Gossip; g != nil {
		cfg.Gossip.Listen = g.Listen
		cfg.Gossip.Advertise = g.Advertise
	}
	if r := f.RPC; r != nil {
		cfg.RPC.Listen = r.Listen
		cfg.RPC.Advertise = r.Advertise
	}
	if c := f.Control; c != nil {
		cfg.Control.Listen = c.Listen
		cfg.Control.Advertise = c.Advertise
	}
	return cfg, nil
}
