package load

import (
	"math"
	"strings"
	"testing"
)

const procStat = `cpu  100 20 30 700 50 0 10 0 0 0
cpu0 50 10 15 350 25 0 5 0 0 0
intr 12345
`

// The aggregate line is the whole point: it already sums every processor, so
// the fraction is of the machine rather than of a core.
func TestParseCPUReadsTheAggregateLineAndCountsIowaitAsIdle(t *testing.T) {
	got, err := ParseCPU(procStat)
	if err != nil {
		t.Fatalf("parsing: %s", err)
	}
	// 100+20+30+700+50+0+10 = 910; idle 700 + iowait 50 = 750.
	if got.Total != 910 {
		t.Errorf("total = %d, want 910", got.Total)
	}
	if got.Idle != 750 {
		t.Errorf("idle = %d, want 750 (idle 700 + iowait 50)", got.Idle)
	}
}

func TestParseCPURejectsWhatItCannotUnderstand(t *testing.T) {
	for name, data := range map[string]string{
		"empty":            "",
		"not the cpu line": "intr 1 2 3 4 5\ncpu 1 2 3 4 5\n",
		"too few fields":   "cpu 1 2 3\n",
		"not a number":     "cpu 1 2 3 four 5\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCPU(data); err == nil {
				t.Fatal("parsed successfully; a malformed /proc/stat must be an error, not a zero reading that reads as idle")
			}
		})
	}
}

func TestUtil(t *testing.T) {
	cases := map[string]struct {
		prev, cur CPUTimes
		want      float64
	}{
		"fully busy":  {CPUTimes{Idle: 0, Total: 0}, CPUTimes{Idle: 0, Total: 100}, 1.0},
		"fully idle":  {CPUTimes{Idle: 0, Total: 0}, CPUTimes{Idle: 100, Total: 100}, 0.0},
		"half busy":   {CPUTimes{Idle: 0, Total: 0}, CPUTimes{Idle: 50, Total: 100}, 0.5},
		"accumulated": {CPUTimes{Idle: 900, Total: 1000}, CPUTimes{Idle: 950, Total: 1100}, 0.5},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Util(c.prev, c.cur); math.Abs(got-c.want) > 1e-9 {
				t.Fatalf("Util = %v, want %v", got, c.want)
			}
		})
	}
}

// Two identical samples mean no time passed, not a fully busy machine. Dividing
// by that delta would be a divide by zero; reporting 1.0 would trip the verdict
// on a stalled clock.
func TestUtilOfAnEmptyIntervalIsIdle(t *testing.T) {
	same := CPUTimes{Idle: 900, Total: 1000}
	if got := Util(same, same); got != 0 {
		t.Fatalf("Util over a zero-length interval = %v, want 0", got)
	}
}

const procMeminfo = `MemTotal:       16384000 kB
MemFree:         1000000 kB
MemAvailable:    4096000 kB
Buffers:          200000 kB
`

func TestParseMemUsed(t *testing.T) {
	got, err := ParseMemUsed(strings.NewReader(procMeminfo))
	if err != nil {
		t.Fatalf("parsing: %s", err)
	}
	// (16384000 - 4096000) / 16384000 = 0.75
	if math.Abs(got-0.75) > 1e-9 {
		t.Fatalf("used = %v, want 0.75", got)
	}
}

// MemAvailable, not MemFree: the kernel's estimate of what can be allocated
// without swapping is the number that answers "can this box take another
// container", and page cache makes MemFree far too pessimistic.
func TestParseMemUsedIgnoresMemFree(t *testing.T) {
	got, err := ParseMemUsed(strings.NewReader(procMeminfo))
	if err != nil {
		t.Fatalf("parsing: %s", err)
	}
	const fromMemFree = (16384000.0 - 1000000.0) / 16384000.0
	if math.Abs(got-fromMemFree) < 1e-9 {
		t.Fatal("used memory was computed from MemFree; a box with a large page cache would read as nearly full and stop taking work")
	}
}

func TestParseMemUsedRejectsWhatItCannotUnderstand(t *testing.T) {
	for name, data := range map[string]string{
		"empty":           "",
		"no MemAvailable": "MemTotal: 100 kB\nMemFree: 50 kB\n",
		"no MemTotal":     "MemAvailable: 50 kB\n",
		"zero MemTotal":   "MemTotal: 0 kB\nMemAvailable: 0 kB\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMemUsed(strings.NewReader(data)); err == nil {
				t.Fatal("parsed successfully; a reading that cannot be computed must be an error, not a 0 that reads as an idle box")
			}
		})
	}
}

// Lines that are not key/value pairs are skipped rather than failing the read:
// /proc/meminfo carries plenty this does not care about.
func TestParseMemUsedSkipsLinesItDoesNotUnderstand(t *testing.T) {
	data := "garbage\n\nHugePagesize: not-a-number kB\nMemTotal: 100 kB\nMemAvailable: 25 kB\n"
	got, err := ParseMemUsed(strings.NewReader(data))
	if err != nil {
		t.Fatalf("parsing: %s", err)
	}
	if math.Abs(got-0.75) > 1e-9 {
		t.Fatalf("used = %v, want 0.75", got)
	}
}

func TestSustainFromEnv(t *testing.T) {
	t.Run("unset is the default", func(t *testing.T) {
		t.Setenv(SustainEnv, "")
		got, err := SustainFromEnv()
		if err != nil || got != DefaultSustain {
			t.Fatalf("got (%d, %v), want (%d, nil)", got, err, DefaultSustain)
		}
	})
	t.Run("set is honoured", func(t *testing.T) {
		t.Setenv(SustainEnv, "3")
		got, err := SustainFromEnv()
		if err != nil || got != 3 {
			t.Fatalf("got (%d, %v), want (3, nil)", got, err)
		}
	})
	for name, v := range map[string]string{"zero": "0", "negative": "-1", "words": "soon"} {
		t.Run(name+" is refused", func(t *testing.T) {
			t.Setenv(SustainEnv, v)
			if _, err := SustainFromEnv(); err == nil {
				t.Fatalf("%q was accepted; a window of zero or less means every sample trips at once", v)
			}
		})
	}
}
