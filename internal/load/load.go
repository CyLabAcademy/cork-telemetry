// Package load samples host CPU and memory utilization from /proc and decides
// whether the machine is overloaded.
//
// It exists as a package rather than as code inside the agent because there
// are two binaries -- the production one and the debug build that reports the
// underlying numbers -- and they must agree. They cannot import each other,
// both being package main, so the rule used to be kept in step by hand: a
// debug build that judged load differently from the binary being debugged
// would be worse than no debug build at all, and nothing enforced it. Here it
// is written once, and both import it.
//
// Both thresholds are fractions of the whole machine, not of one core. The
// aggregate "cpu" line of /proc/stat already sums every processor, so 0.90 on
// a four-core box means about three and a half cores busy and a single pinned
// core reads 0.25. That is the right question to ask of a machine being
// offered more work: one saturated core says nothing about whether there is
// room for another container.
package load

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// Hysteresis band: trip at the high mark, only recover below the low mark,
	// so a machine sitting on the threshold does not flap on and off the
	// scheduler.
	HighThreshold = 0.90
	LowThreshold  = 0.80

	// SampleInterval is how often the agent takes a reading. It is cheap --
	// two small /proc reads -- and its speed is what lets the CPU rule below
	// have a usefully fine-grained run counter.
	SampleInterval = 500 * time.Millisecond

	// DefaultSustain is how many consecutive samples CPU must spend past a
	// mark before the verdict moves. Four seconds at the sample interval, and
	// deliberately reluctant.
	//
	// CPU is the weaker of the two signals. On a challenge host a busy
	// processor is very often the workload doing exactly what it is for --
	// brute forcers, crypto challenges, whatever a student has just run -- and
	// it says much less about whether the box can take another container than
	// memory does. Memory is what actually runs out, and it still trips on a
	// single sample, so little is lost by making CPU slow to speak.
	//
	// Nor does waiting cost much: cork polls the agent every few seconds, so
	// the poll interval already dominates detection, and the verdict latches
	// once tripped rather than being a slice a poll might read between. Real
	// saturation holds for minutes and is caught whatever this is set to; what
	// the window buys is not mistaking a converge, an image unpack or a reaper
	// pass for a machine in trouble.
	DefaultSustain = 8

	// SustainEnv overrides DefaultSustain, so the number can be measured on a
	// real worker instead of argued about here.
	SustainEnv = "TELEMETRY_CPU_SUSTAIN"
)

// SustainFromEnv returns the configured CPU window, or an error describing a
// value that cannot be one.
func SustainFromEnv() (int, error) {
	v := os.Getenv(SustainEnv)
	if v == "" {
		return DefaultSustain, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid %s=%q: must be a positive number of samples", SustainEnv, v)
	}
	return n, nil
}

// State carries what each metric has been doing between samples, which is what
// lets the two be judged by different rules. It is owned by the sampling
// goroutine and touched by nothing else.
type State struct {
	// Sustain is how many consecutive samples CPU must spend past a mark
	// before the verdict moves. Zero means DefaultSustain.
	Sustain int

	memOver bool
	cpuOver bool
	runHigh int // consecutive CPU samples above the high mark
	runLow  int // consecutive CPU samples below the low mark
}

func (s *State) sustain() int {
	if s.Sustain < 1 {
		return DefaultSustain
	}
	return s.Sustain
}

// Next folds one sample in and returns the verdict. A machine is overloaded
// when either metric says so; they are independent, and neither masks the
// other.
//
// Memory is a level: it climbs as containers start, does not come back down on
// its own, and a box over the mark will still be over it a minute later, so one
// sample is enough and memory handed back is capacity returned. CPU over a
// single sample window is a peak, and on a worker the peaks ARE the work --
// unpacking an image and starting a container pins cores for a few hundred
// milliseconds by nature -- so it must hold past a mark for Sustain samples
// before the verdict moves.
func (s *State) Next(cpu, mem float64) bool {
	if s.memOver {
		s.memOver = mem >= LowThreshold
	} else {
		s.memOver = mem > HighThreshold
	}

	// A sample inside the band neither confirms nor clears anything, so it
	// breaks both runs and leaves the verdict as it was. That is what the band
	// is for.
	switch {
	case cpu > HighThreshold:
		s.runHigh++
		s.runLow = 0
	case cpu < LowThreshold:
		s.runLow++
		s.runHigh = 0
	default:
		s.runHigh, s.runLow = 0, 0
	}
	if s.cpuOver {
		if s.runLow >= s.sustain() {
			s.cpuOver = false
		}
	} else if s.runHigh >= s.sustain() {
		s.cpuOver = true
	}

	return s.memOver || s.cpuOver
}

// CPUTimes is one reading of the aggregate CPU counters.
type CPUTimes struct{ Idle, Total uint64 }

// ReadCPU samples /proc/stat.
func ReadCPU() (CPUTimes, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return CPUTimes{}, err
	}
	return ParseCPU(string(data))
}

// ParseCPU reads the aggregate "cpu" line, which is the first line of
// /proc/stat and already sums every processor. Idle counts both the idle and
// iowait fields.
func ParseCPU(data string) (CPUTimes, error) {
	line := strings.SplitN(data, "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return CPUTimes{}, fmt.Errorf("unexpected /proc/stat format: %q", line)
	}

	var t CPUTimes
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return CPUTimes{}, err
		}
		t.Total += v
		if i == 3 || i == 4 { // idle, iowait
			t.Idle += v
		}
	}
	return t, nil
}

// Util returns the busy fraction between two samples.
func Util(prev, cur CPUTimes) float64 {
	dTotal := cur.Total - prev.Total
	if dTotal == 0 {
		return 0
	}
	dIdle := cur.Idle - prev.Idle
	return float64(dTotal-dIdle) / float64(dTotal)
}

// ReadMemUsed samples /proc/meminfo.
func ReadMemUsed() (float64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return ParseMemUsed(f)
}

// ParseMemUsed returns used memory as a fraction of total, using MemAvailable
// as the kernel's estimate of what can be allocated without swapping.
func ParseMemUsed(r io.Reader) (float64, error) {
	var total, avail uint64
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, val, ok := parseMeminfoLine(sc.Text())
		if !ok {
			continue
		}
		switch key {
		case "MemTotal":
			total, haveTotal = val, true
		case "MemAvailable":
			avail, haveAvail = val, true
		}
		if haveTotal && haveAvail {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if !haveTotal || total == 0 {
		return 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
	}
	if !haveAvail {
		return 0, fmt.Errorf("MemAvailable not found in /proc/meminfo")
	}
	return float64(total-avail) / float64(total), nil
}

// parseMeminfoLine splits a line like "MemTotal:  16384000 kB" into its key and
// numeric value (in kB).
func parseMeminfoLine(line string) (key string, val uint64, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", 0, false
	}
	key = strings.TrimSuffix(fields[0], ":")
	v, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return key, v, true
}
