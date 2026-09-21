package load

import "testing"

// feed runs a series of samples through one state and returns the last verdict.
func feed(s *State, samples [][2]float64) bool {
	var out bool
	for _, sm := range samples {
		out = s.Next(sm[0], sm[1])
	}
	return out
}

// rep is n samples of the same reading.
func rep(n int, cpu, mem float64) [][2]float64 {
	out := make([][2]float64, n)
	for i := range out {
		out[i] = [2]float64{cpu, mem}
	}
	return out
}

// Memory is a level. It climbs as containers start, does not come back down on
// its own, and a box over the mark will still be over it a minute later, so one
// sample is enough to act on.
func TestMemoryTripsOnASingleSample(t *testing.T) {
	var s State
	if !s.Next(0.10, 0.95) {
		t.Fatal("a worker over the memory mark was not reported overloaded until a second sample: memory is the metric that means stop placing")
	}
}

// And memory handed back is capacity returned, so it clears as promptly, once
// it is under the low mark rather than merely under the high one.
func TestMemoryClearsAtTheLowMarkAndNotBefore(t *testing.T) {
	var s State
	s.Next(0.10, 0.95)
	if !s.Next(0.10, 0.85) {
		t.Fatal("memory inside the hysteresis band cleared the verdict; the band exists so a box on the threshold does not flap")
	}
	if s.Next(0.10, 0.70) {
		t.Fatal("memory back under the low mark did not clear the verdict")
	}
}

// The regression this rule exists for. Unpacking an image and starting a
// container pins cores for a few hundred milliseconds, which is the box doing
// its job, not the box in trouble. Tripping on one of those takes a healthy
// worker out of a two-box fleet exactly when a burst needs it.
func TestOneCPUSpikeIsNotOverload(t *testing.T) {
	var s State
	for i := 0; i < DefaultSustain-1; i++ {
		if s.Next(1.0, 0.10) {
			t.Fatalf("a CPU spike %d sample(s) long was reported as overload; it has to hold for %d", i+1, DefaultSustain)
		}
	}
}

// A burst of launches is spikes separated by gaps, and no run of spikes ever
// reaches the threshold. This is the shape an idle-ish worker actually sees.
func TestALaunchBurstNeverTripsCPU(t *testing.T) {
	var s State
	for i := 0; i < 50; i++ {
		// Two samples of a launch, then the gap after it -- and the verdict is
		// checked at EVERY sample, not just once the burst has passed. cork
		// polls on its own clock and reads whatever the latest sample left
		// behind, so a verdict that is only correct in the gaps is not
		// correct. Asserting after the quiet sample would pass under the very
		// rule this replaces, which clears the moment CPU drops.
		for j, cpu := range []float64{1.0, 1.0, 0.05} {
			if s.Next(cpu, 0.20) {
				t.Fatalf("a burst of short launch spikes was reported as overload at round %d, sample %d (cpu %.2f)", i, j, cpu)
			}
		}
	}
}

// Sustained saturation is real, and must be caught.
func TestSustainedCPUTrips(t *testing.T) {
	var s State
	if !feed(&s, rep(DefaultSustain, 0.95, 0.10)) {
		t.Fatalf("CPU over the mark for %d consecutive samples was not reported as overload", DefaultSustain)
	}
}

// Clearing is symmetric: it has to stay down as long as it had to stay up,
// so a saturated box does not bounce back into placement on one quiet sample.
func TestCPUClearsOnlyWhenItHolds(t *testing.T) {
	var s State
	feed(&s, rep(DefaultSustain, 0.95, 0.10))
	for i := 0; i < DefaultSustain-1; i++ {
		if !s.Next(0.05, 0.10) {
			t.Fatalf("CPU cleared after %d quiet sample(s); it has to hold for %d", i+1, DefaultSustain)
		}
	}
	if s.Next(0.05, 0.10) {
		t.Fatal("CPU did not clear after holding under the low mark")
	}
}

// A sample inside the band is not evidence either way, so it holds the verdict
// and breaks the run. That is what the band is for.
func TestABandSampleHoldsTheVerdictAndBreaksTheRun(t *testing.T) {
	var s State
	feed(&s, rep(DefaultSustain-1, 0.95, 0.10)) // one short of tripping
	if s.Next(0.85, 0.10) {
		t.Fatal("a sample inside the band tripped the verdict")
	}
	if s.Next(0.95, 0.10) {
		t.Fatal("the run was not broken by the band sample: one more high sample should not be enough")
	}
}

// The window is tunable so the right value can be measured on a real worker
// rather than argued about. The rule has to honour it, or the override is
// decoration.
func TestCPUSustainIsHonoured(t *testing.T) {
	s := State{Sustain: 2}
	if s.Next(0.95, 0.10) {
		t.Fatal("CPU tripped on the first sample with a sustain of 2")
	}
	if !s.Next(0.95, 0.10) {
		t.Fatal("CPU did not trip on the second sample with a sustain of 2")
	}
}

// An unset window falls back to the default rather than to zero, which would
// make every sample past a mark trip at once -- the behaviour this rule exists
// to replace.
func TestAZeroSustainFallsBackToTheDefault(t *testing.T) {
	var s State // Sustain left at zero
	if s.Next(0.95, 0.10) {
		t.Fatal("a State with no Sustain tripped on one sample: the zero value must mean the default, not no window at all")
	}
	if !feed(&s, rep(DefaultSustain-1, 0.95, 0.10)) {
		t.Fatalf("a State with no Sustain did not trip after %d samples", DefaultSustain)
	}
}

// The two metrics are independent. Memory over the mark must not be masked by
// CPU being fine, which is the case that matters on a small worker: memory is
// what runs out, and CPU recovering says nothing about it.
func TestMemoryIsNotMaskedByQuietCPU(t *testing.T) {
	var s State
	if !feed(&s, rep(10, 0.01, 0.95)) {
		t.Fatal("a worker out of memory was reported healthy because its CPU was idle")
	}
}

// And the mirror: CPU saturation stands on its own with memory low.
func TestSustainedCPUIsNotMaskedByFreeMemory(t *testing.T) {
	var s State
	if !feed(&s, rep(DefaultSustain, 0.99, 0.05)) {
		t.Fatal("a saturated worker was reported healthy because it had memory free")
	}
}

// A box that is genuinely in trouble on both stays overloaded until BOTH have
// recovered, since either one alone is reason enough to stop placing.
func TestBothMustRecover(t *testing.T) {
	var s State
	feed(&s, rep(DefaultSustain, 0.95, 0.95))
	if !feed(&s, rep(DefaultSustain, 0.05, 0.95)) {
		t.Fatal("CPU recovering cleared the verdict while memory was still over the mark")
	}
	if feed(&s, rep(DefaultSustain, 0.05, 0.05)) {
		t.Fatal("the verdict did not clear once both metrics had recovered")
	}
}

// ------------------------------------------------- is the verdict worth serving

// An idle box settles on its first sample. The wait exists for boxes that come
// up hot, and must cost nothing for the ordinary case.
func TestAnIdleBoxSettlesImmediately(t *testing.T) {
	var s State
	s.Next(0.05, 0.20)
	if !s.Settled() {
		t.Fatal("an idle box was not settled after one sample: the wait is meant for a box that comes up hot, not for every start")
	}
}

// The case the wait exists for. The run counters are process state, so an agent
// restarted on a saturated box starts at cpuOver=false and would report "not
// overloaded" for the whole Sustain window -- indistinguishable from a measured
// verdict, and enough to green-light a worker that is in fact pegged.
func TestASaturatedBoxIsUnsettledUntilItTrips(t *testing.T) {
	s := State{Sustain: 4}
	for i := 1; i < 4; i++ {
		s.Next(0.99, 0.20)
		if s.Settled() {
			t.Fatalf("settled after %d saturated sample(s) with a sustain of 4: the agent would report this box healthy", i)
		}
	}
	if !s.Next(0.99, 0.20) || !s.Settled() {
		t.Fatal("the trip did not settle the verdict")
	}
}

// And it settles the moment the box stops sustaining, without waiting out the
// window: whatever it was doing, it is not tripping now.
func TestAClimbThatBreaksSettlesAtOnce(t *testing.T) {
	s := State{Sustain: 4}
	s.Next(0.99, 0.20)
	if s.Settled() {
		t.Fatal("settled while still climbing")
	}
	s.Next(0.10, 0.20)
	if !s.Settled() {
		t.Fatal("a box that stopped sustaining did not settle")
	}
}

// Memory never waits behind the CPU window. It is a level, one sample settles
// it, and it is the axis that actually exhausts these boxes -- a box that comes
// up over on memory must be reported at once even though CPU is still climbing.
func TestMemorySettlesEvenWhileCPUIsStillClimbing(t *testing.T) {
	s := State{Sustain: 8}
	if !s.Next(0.99, 0.95) {
		t.Fatal("memory over the mark did not report overloaded")
	}
	if !s.Settled() {
		t.Fatal("a box out of memory waited behind the CPU window; that is the axis that must never wait")
	}
}
