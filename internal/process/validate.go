package process

import (
	"regexp"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/logmgr"
)

var nameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,62}$`)
var groupRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func ValidateSpec(s ProcessSpec) error {
	if s.Name == "" || !nameRE.MatchString(s.Name) {
		return errcode.E(errcode.INVALID, "name")
	}
	if s.Command == "" {
		return errcode.E(errcode.INVALID, "command")
	}
	if s.Instances < 1 {
		return errcode.E(errcode.INVALID, "instances")
	}
	seen := make(map[string]struct{}, len(s.Dependencies))
	for _, d := range s.Dependencies {
		if _, ok := seen[d.ProcessName]; ok {
			return errcode.E(errcode.INVALID, "duplicate dependency")
		}
		seen[d.ProcessName] = struct{}{}
	}
	if s.Restart.MaxRetries > 0 && s.Restart.RetryWindow == 0 {
		return errcode.E(errcode.INVALID, "retry window")
	}
	if s.Restart.Backoff.Multiplier != 0 && s.Restart.Backoff.Multiplier < 1 {
		return errcode.E(errcode.INVALID, "backoff multiplier")
	}
	g := strings.TrimSpace(s.Group)
	if g != "" && !groupRE.MatchString(g) {
		return errcode.E(errcode.INVALID, "group")
	}
	if err := logmgr.ValidateDirectory(s.Log.Directory, ""); err != nil {
		return err
	}
	return nil
}
