//go:build linux

package api

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func readProcStat(pid int) (cpuPercent int32, memBytes int64, ok bool) {
	if pid <= 0 {
		return 0, 0, false
	}
	statb, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, false
	}
	statusb, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, false
	}
	mem, memOK := parseVmRSS(statusb)
	if !memOK {
		return 0, 0, false
	}
	cpu, cpuOK := parseCPUPercent(statb)
	if !cpuOK {
		return 0, 0, false
	}
	return cpu, mem, true
}

func parseVmRSS(status []byte) (int64, bool) {
	for _, line := range bytes.Split(status, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("VmRSS:")) {
			continue
		}
		fields := strings.Fields(string(line))
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

func parseCPUPercent(stat []byte) (int32, bool) {
	rparen := bytes.LastIndexByte(stat, ')')
	if rparen < 0 || rparen+2 >= len(stat) {
		return 0, false
	}
	fields := strings.Fields(string(stat[rparen+2:]))
	// after comm: state(3), ..., utime(14), stime(15), ..., starttime(22)
	// fields[0] is state (field 3)
	if len(fields) < 20 {
		return 0, false
	}
	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	start, err3 := strconv.ParseUint(fields[19], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	hz := clockTicks()
	if hz <= 0 {
		return 0, false
	}
	upb, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	upFields := strings.Fields(string(upb))
	if len(upFields) < 1 {
		return 0, false
	}
	uptime, err := strconv.ParseFloat(upFields[0], 64)
	if err != nil {
		return 0, false
	}
	elapsed := uptime - float64(start)/float64(hz)
	if elapsed <= 0 {
		return 0, true
	}
	cpu := float64(utime+stime) / float64(hz) / elapsed * 100
	if cpu < 0 {
		cpu = 0
	}
	return int32(cpu), true
}

func clockTicks() int64 {
	hz, err := unix.Sysconf(unix.SC_CLK_TCK)
	if err != nil || hz <= 0 {
		return 100
	}
	return hz
}
