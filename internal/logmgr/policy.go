package logmgr

import "github.com/qleelulu/procmesh/internal/errcode"

type Policy struct {
	WarnPercent         int
	CleanupPercent      int
	EmergencyPercent    int
	AutoDelete          bool
	EmergencyStopWrites bool
}

func DefaultPolicy() Policy {
	return Policy{
		WarnPercent:         85,
		CleanupPercent:      90,
		EmergencyPercent:    95,
		AutoDelete:          false,
		EmergencyStopWrites: true,
	}
}

func (p Policy) Validate() error {
	if p.WarnPercent < 1 || p.WarnPercent > 100 ||
		p.CleanupPercent < 1 || p.CleanupPercent > 100 ||
		p.EmergencyPercent < 1 || p.EmergencyPercent > 100 {
		return errcode.E(errcode.INVALID, "disk percent out of range")
	}
	if !(p.WarnPercent < p.CleanupPercent && p.CleanupPercent < p.EmergencyPercent) {
		return errcode.E(errcode.INVALID, "disk percents must be warn < cleanup < emergency")
	}
	return nil
}
