package process_test

import (
	"testing"
	"time"

	"github.com/qleelulu/procmesh/internal/errcode"
	"github.com/qleelulu/procmesh/internal/process"
)

func TestApplyDefaults_Nil(t *testing.T) {
	process.ApplyDefaults(nil)
}

func TestApplyDefaults_InstancesOne(t *testing.T) {
	s := process.ProcessSpec{Name: "n", Command: "/bin/true"}
	process.ApplyDefaults(&s)
	if s.Instances != 1 || s.StopSignal != "SIGTERM" || s.Restart.Mode != process.RestartOnFailure {
		t.Fatalf("%+v", s)
	}
}

func TestValidateSpec_RejectsEmptyNameAndZeroInstances(t *testing.T) {
	err := process.ValidateSpec(process.ProcessSpec{Command: "/bin/true"})
	if !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestMakeInstanceID(t *testing.T) {
	got := process.MakeInstanceID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", 3)
	if got != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:3" {
		t.Fatalf("got %q", got)
	}
}

func validSpec() process.ProcessSpec {
	return process.ProcessSpec{
		Name:      "web",
		Command:   "/bin/true",
		Instances: 1,
	}
}

func TestValidateSpec_AcceptsMinimalValid(t *testing.T) {
	if err := process.ValidateSpec(validSpec()); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSpec_RejectsBadName(t *testing.T) {
	s := validSpec()
	s.Name = "1bad"
	if err := process.ValidateSpec(s); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSpec_RejectsEmptyCommand(t *testing.T) {
	s := validSpec()
	s.Command = ""
	if err := process.ValidateSpec(s); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSpec_RejectsZeroInstances(t *testing.T) {
	s := validSpec()
	s.Instances = 0
	if err := process.ValidateSpec(s); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSpec_RejectsDuplicateDependencies(t *testing.T) {
	s := validSpec()
	s.Dependencies = []process.Dependency{
		{ProcessName: "mysql"},
		{ProcessName: "mysql"},
	}
	if err := process.ValidateSpec(s); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSpec_RequiresRetryWindowWhenMaxRetriesSet(t *testing.T) {
	s := validSpec()
	s.Restart.MaxRetries = 3
	if err := process.ValidateSpec(s); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
	s.Restart.RetryWindow = time.Minute
	if err := process.ValidateSpec(s); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSpec_RejectsBackoffMultiplierBelowOne(t *testing.T) {
	s := validSpec()
	s.Restart.Backoff.Multiplier = 0.5
	if err := process.ValidateSpec(s); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSpec_AllowsAnyStartupPriority(t *testing.T) {
	s := validSpec()
	s.StartupPriority = -7
	if err := process.ValidateSpec(s); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSpec_RejectsInvalidGroup(t *testing.T) {
	s := validSpec()
	s.Group = "bad group"
	if err := process.ValidateSpec(s); !errcode.Is(err, errcode.INVALID) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSpec_AcceptsEmptyAndValidGroup(t *testing.T) {
	s := validSpec()
	if err := process.ValidateSpec(s); err != nil {
		t.Fatal(err)
	}
	s.Group = "finance"
	if err := process.ValidateSpec(s); err != nil {
		t.Fatal(err)
	}
	s.Group = "  finance  "
	if err := process.ValidateSpec(s); err != nil {
		t.Fatal(err)
	}
}
