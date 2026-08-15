//go:build !linux

package api

func readProcStat(int) (cpuPercent int32, memBytes int64, ok bool) {
	return 0, 0, false
}
