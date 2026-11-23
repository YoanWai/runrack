package hoststat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/YoanWai/runrack/internal/state"
)

type cpuTimes struct{ busy, total uint64 }

// Sampler reads host load, CPU and memory. CPU percent needs two samples.
type Sampler struct {
	procRoot string
	previous cpuTimes
}

func NewSampler(procRoot string) *Sampler { return &Sampler{procRoot: procRoot} }

func (s *Sampler) Sample() (state.HostStats, error) {
	var stats state.HostStats
	stats.Hostname, _ = os.Hostname()
	loadavg, err := os.ReadFile(filepath.Join(s.procRoot, "loadavg"))
	if err != nil {
		return stats, err
	}
	if stats.Load1, stats.Load5, stats.Load15, err = ParseLoadavg(string(loadavg)); err != nil {
		return stats, err
	}
	stat, err := os.ReadFile(filepath.Join(s.procRoot, "stat"))
	if err != nil {
		return stats, err
	}
	times, cpus, err := ParseStat(string(stat))
	if err != nil {
		return stats, err
	}
	stats.CPUs = cpus
	if s.previous.total != 0 && times.total > s.previous.total {
		stats.CPUPct = 100 * float64(times.busy-s.previous.busy) / float64(times.total-s.previous.total)
	}
	s.previous = times
	meminfo, err := os.ReadFile(filepath.Join(s.procRoot, "meminfo"))
	if err != nil {
		return stats, err
	}
	if stats.MemUsed, stats.MemTotal, err = ParseMeminfo(string(meminfo)); err != nil {
		return stats, err
	}
	uptime, err := os.ReadFile(filepath.Join(s.procRoot, "uptime"))
	if err != nil {
		return stats, err
	}
	seconds, err := strconv.ParseFloat(strings.Fields(string(uptime))[0], 64)
	if err != nil {
		return stats, fmt.Errorf("uptime: %w", err)
	}
	stats.Uptime = state.Duration(time.Duration(seconds * float64(time.Second)))
	return stats, nil
}

func ParseLoadavg(s string) (load1, load5, load15 float64, err error) {
	fields := strings.Fields(s)
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("loadavg: too few fields")
	}
	values := make([]float64, 3)
	for i := range values {
		if values[i], err = strconv.ParseFloat(fields[i], 64); err != nil {
			return 0, 0, 0, fmt.Errorf("loadavg: %w", err)
		}
	}
	return values[0], values[1], values[2], nil
}

// ParseStat sums the aggregate cpu line. Idle and iowait count as not busy.
func ParseStat(s string) (cpuTimes, int, error) {
	var times cpuTimes
	cpus := 0
	found := false
	for line := range strings.SplitSeq(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			cpus++
			continue
		}
		found = true
		for i, field := range fields[1:min(len(fields), 9)] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return cpuTimes{}, 0, fmt.Errorf("stat cpu: %w", err)
			}
			times.total += value
			if i != 3 && i != 4 {
				times.busy += value
			}
		}
	}
	if !found {
		return cpuTimes{}, 0, errors.New("stat: no cpu line")
	}
	return times, cpus, nil
}

func ParseMeminfo(s string) (used, total int64, err error) {
	values := map[string]int64{}
	for line := range strings.SplitSeq(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		values[name] = kb * 1024
	}
	total, ok := values["MemTotal"]
	available, ok2 := values["MemAvailable"]
	if !ok || !ok2 {
		return 0, 0, errors.New("meminfo: MemTotal or MemAvailable missing")
	}
	return total - available, total, nil
}
