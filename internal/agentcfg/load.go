package agentcfg

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/logmgr"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Disk   logmgr.Policy
	Gossip Gossip
	RPC    RPC
}

type Gossip struct {
	Listen    string
	Advertise string
}

type RPC struct {
	Listen    string
	Advertise string
}

type file struct {
	Disk   *diskFile   `yaml:"disk"`
	Gossip *gossipFile `yaml:"gossip"`
	RPC    *rpcFile    `yaml:"rpc"`
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
		return Config{Disk: logmgr.DefaultPolicy()}, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if required {
				return Config{}, errcode.E(errcode.INVALID, "config file not found")
			}
			return Config{Disk: logmgr.DefaultPolicy()}, nil
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
	cfg := Config{Disk: p}
	if g := f.Gossip; g != nil {
		cfg.Gossip.Listen = g.Listen
		cfg.Gossip.Advertise = g.Advertise
	}
	if r := f.RPC; r != nil {
		cfg.RPC.Listen = r.Listen
		cfg.RPC.Advertise = r.Advertise
	}
	return cfg, nil
}
