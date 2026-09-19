# Standalone Telemetry Module

This telemetry module is for use with the cork orchestrator (forked from cmgr). It runs a simple go server that returns a json with the format {"overloaded": True/False} depending on the status of the server.

To hit the endpoint do http://<ip_OR_URL>:2136/health

By default it runs on port 2136 so make sure cmgr doesn't assign this port for a challenge! This setting is configurable

Until it has a verdict worth giving it answers 503 rather than guessing, so a
fresh agent is never mistaken for a healthy machine, and neither is a box the
agent cannot yet judge. cork counts that as a missed poll. An idle box settles
on its first sample; a box crossing the CPU high mark without holding it waits,
because the agent will not call a box healthy while it cannot tell a sustained
trip from a spike — so a worker hovering near the mark shows this now and
again, for the life of the process, not only at startup. Memory never waits —
one sample settles it.

It returns overloaded when either cpu or ram crosses 90%, and does not go back
to fine until that metric drops below 80%. The gap between the two marks is
what stops a server sitting on the threshold from flapping on and off the
scheduler.

The two metrics are judged differently, because they behave differently:

- **Memory trips at once**, in both directions. It is a level — it climbs as
  containers start, does not come back down by itself, and a box over the mark
  will still be over it a minute later. One sample is enough to act on, and
  memory handed back is capacity returned.
- **CPU has to be sustained**, eight consecutive samples (four seconds) past a
  mark before the verdict moves either way, and deliberately reluctant. On a
  challenge host a busy processor is very often the workload doing exactly
  what it is for, and it says much less about whether the box can take another
  container than memory does. Waiting costs little: cork polls every few
  seconds anyway, and real saturation holds for minutes. Set
  `TELEMETRY_CPU_SUSTAIN` to a different number of samples to tune it.

Both figures are fractions of the **whole machine**, not of one core — the
aggregate `cpu` line of `/proc/stat` already sums every processor. So 0.90 on a
four-core server means roughly three and a half cores busy, and a single pinned
core reads 0.25. That is the right question to ask of a box being offered more
work: one saturated core says nothing about whether there is room for another
container.

The source code within the debug directory is for a custom debugger build that will expose more system health values and log them to stderr. By default telemetry only exposes whether or not the system is considered overloaded and does not log anything.

### Configuration

The preconfigured telemetry.service file provides telemetry as a systemd service. It defaults to port 2136 and expects the telemetry binary at /usr/local/bin/telemetry. Place the file in /etc/systemd/system/.

### Ansible Support

Eventually, we want ansible to pull an artifact from this build and then automatically install this server on the challenge workers, but that is for the future.

### HTTPS Support

If we want HTTPS support then we will have this guy serve using the docker certs as well
