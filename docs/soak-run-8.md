# Soak run 8

The Phase 1 gate's 72-hour measurement. Nothing was injected, so every
incident Sentinel opened is a false alarm. The run finished green.

| | |
|---|---|
| Workflow | [Soak, run 8](https://github.com/karimzakzouk/sentinel/actions/runs/42955246454) |
| Job | [soak on `sentinel-azure-1`](https://github.com/karimzakzouk/sentinel/actions/runs/42955246454/job/130308680913) (`self-hosted`, `sentinel-lab`) |
| Revision | `af727ef8d423e2f49e9dbaabdf735fd2b699f845` ("Request soak after Go bump and RoleBinding fix") |
| Request | `.github/requests/soak`, `hours: 72` |
| Conclusion | success, on the first attempt |
| Queued | 2026-11-11 03:49:24 UTC |
| Runner started | 2026-11-11 04:10:04 UTC |
| Watch | 2026-11-11 04:16:29 UTC to 2026-11-14 04:17:04 UTC |
| Job finished | 2026-11-14 04:17:16 UTC |
| Artifact | `soak` (id 14097889075), kept until 2026-12-14: `soak-report.txt`, `soak-progress.txt`, `controller.log` |

`main` moved while the cluster was up. `876f302` (04:49 UTC, settings
reconciliation) and `bbc601a` (05:21 UTC, actor rollback persistence) are
not in the binary this run watched. The job checks out the commit that
started it and does not redeploy.

## What ran

`make e2e-up` and `make boutique-up`, then `hack/soak.sh start`, which
reinstalls Sentinel with `test/soak/values-sentinel.yaml` and the
production detection settings: window 5m, tick 15s, incident gap 5m,
warm-up 30m, error sample every 1m. The chart's false-alarm budget stays
1 per workload per week. Watched namespaces are `e2e-smoke` and
`boutique`. Online Boutique's load generator is the noise: `USERS=10`,
`RATE=1`, one replica, constant for the whole run. The smoke workloads
are `web`, `front`, `backend` and `store`. Actor and Lab are on; log
level is debug. Every step of the job succeeded, including deleting the
kind cluster at the end.

The retained evaluations use alpha 2.067×10⁻⁶. That is decision 17's
split of a budget of 1 across a 15-second tick and the twelve fact kinds,
so the production budget was still the one in force at the end of the run.

## Pass and fail

The gate in [phase-1.md](plan/phase-1.md), and the same line in the build
plan, is two measurements:

1. **Detection precision and recall, and localization accuracy, on the
   development fault pool**, scored by `harness score`. Already recorded
   in [STATUS.md](STATUS.md): 9 of 9 faults detected, precision 1.00, the
   root cause ranked first every time, class, blamed change and decision
   right every time, median 5 seconds to detect, no false alarm in the
   three quiet runs. This soak does not re-score that pool. A quiet run
   cannot speak to recall, which is the revisit condition on decision 17.

2. **False alarms per service per week in a 72-hour noisy soak.** The
   plan's own words are that this is measured here, and that a shorter
   run is only an estimate until a three-day machine exists. This run is
   that measurement. The build plan says "measured", and does not set a
   second numeric fail line. The budget of 1 per workload per week is the
   knob that sets alpha (decision 17), and the numbers below are what the
   soak produced against it.

Both written criteria are met. The gate does not also require seasonal
detectors: slice 4 left those until this soak could say whether the ratio
floor was enough. That question is proposed decision 123.

| Quantity | Value |
|---|---|
| Watch length | 72h 0m, from the clock `hack/soak.sh start` wrote to the final report |
| Reports | 432, one at the start and then every 10 minutes. The gap is 602 seconds except the last, 303 seconds, where the 72-hour clock ran out. No report is missing |
| Workloads sampled | 16: twelve Deployments in `boutique` (including the load generator and `redis-cart`) and `web`, `front`, `backend`, `store` in `e2e-smoke` |
| In-window false alarms | 1 |
| Per workload per week, the 16 together | 0.15 (1 alarm × 168/72 ÷ 16) |
| The one workload that alarmed, point estimate | 2.33 per week |
| That workload, 95% Poisson interval | 0.059 to 13.0 per week |
| Each quiet workload, 95% upper bound | 8.61 per week |
| Budget the detector is built to | 1 per workload per week, so 16 per week across this set |

The interval is the Garwood exact interval: for k events in T = 72/168
weeks, the 95% limits on the weekly rate are ½χ²(0.025, 2k) / T and
½χ²(0.975, 2k+2) / T, and the lower limit is 0 when k is 0. One event in
three days cannot pin a weekly rate. The point estimate on
`paymentservice` sits above the budget of 1, and the interval includes
the budget and values well above it. The other fifteen workloads
contributed none. The cluster rate, 2.33 false alarms per week across 16
workloads, sits under the 16 the budget allows. Nothing here says to
recalibrate alpha.

## The one in-window incident

`boutique/paymentservice`, class `runtime.errors` at 93%, cause the
service itself at 100%, would escalate. Opened 2026-11-13 21:03 UTC.
Still open at the 21:10:31 report, resolved by the 21:20:33 report, so it
had closed within 18 minutes of opening. Escalating, rather than rolling back,
is what decision 37 specifies when pods are up and no change is to blame.
It did not open again, and the same hour on 11 and 12 November was quiet.

The final `soak-report.txt` is that one row and `false_alarms=1`.

## Before the clock

The first report passed `--since 0s`. A zero duration means no cutoff
(`internal/cli/incidents.go`), so that one report listed the whole ledger:

| Opened (UTC) | Workload | Class | Would do |
|---|---|---|---|
| Nov 11 04:15 | `boutique/recommendationservice` | `runtime.errors` (39%) | escalate |
| Nov 11 04:14 | `boutique/emailservice` | `runtime.errors` (39%) | escalate |
| Nov 11 04:14 | `boutique/currencyservice` | `runtime.errors` (39%) | escalate |
| Nov 11 04:14 | `e2e-smoke/front` | `runtime.errors` (39%) | escalate |

The soak clock is 04:16:29, and `hack/soak.sh start` restarts the
controller as it writes that clock. These four opened during bring-up,
under the end-to-end values (a 1-minute window and a 3-minute warm-up),
one to two minutes before the production settings took over. The 04:26
report, the first with a positive `--since`, lists none of them, and the
final 72-hour query does not either: they opened before its cutoff. They
are bring-up, resolved in under ten minutes, and they are not in the
false-alarm count above.

## What the node did for three days

Every progress report after the first records `uptime` and `docker stats`
for the kind node containers. That is the load signal kept for the whole
72 hours. Per-workload error counts were not: `controller.log` in the
artifact is the debug log still in the pod at collection, 02:11:06 to
04:17:04 UTC on 14 November (2,838 lines). The incident at 21:03 on 13
November is not in it.

Kind worker CPU, percent of the VM, mean of the samples in that UTC hour
(about 18 samples, three days):

| Hour | CPU | Hour | CPU | Hour | CPU | Hour | CPU |
|---|---|---|---|---|---|---|---|
| 00 | 12.4 | 06 | 13.0 | 12 | 12.1 | 18 | 13.6 |
| 01 | 12.1 | 07 | 13.2 | 13 | 10.5 | 19 | 13.4 |
| 02 | 12.6 | 08 | 13.6 | 14 | 12.5 | 20 | 15.0 |
| 03 | 12.0 | 09 | 14.2 | 15 | 11.2 | 21 | 13.5 |
| 04 | 11.6 | 10 | 15.5 | 16 | 10.9 | 22 | 12.8 |
| 05 | 12.0 | 11 | 12.5 | 17 | 12.3 | 23 | 12.6 |

The hourly means span 10.5% to 15.5%, a factor of 1.47. The control
plane's hourly means span 10.1% to 11.4%. Load average for the hour stays
between 0.32 and 0.57. Day means of worker CPU are 12.6% (11 November,
from 04:26), 12.6% (12 November), 12.9% (13 November) and 12.6% (14
November, through 04:12).

The mild bump does not repeat. Worker CPU at 10:00 UTC was 11.8%, then
15.9%, then 18.7% across the three days. At 20:00 UTC it was 17.0%, then
18.5%, then 10.0%. A daily cycle would put the same hour in the same
place each day. This one wanders, which fits a constant load generator
on a burstable 4-vCPU VM more than it fits a schedule.

Worker memory climbed from 915 MiB at 04:26 on 11 November to about
1,107 MiB at the end, and it did not dip with the hour. No incident
tracks that climb.

## The ratio floor, from the retained tail

Decision 36 requires a doubling as well as significance before an error
series alarms. In the last two hours, with about 64 hours of exposure
already learned:

- `paymentservice` held about 116 errors over 63.7 hours of exposure
  (1.8 an hour). The window count was 1 against an expected 0.20, tail
  probability about 0.15, not in an episode.
- `frontend` reached 37 against 24 expected (1.54×) at 03:38 UTC, tail
  probability 0.0088, still far above alpha, and the ratio was under 2,
  so it did not alarm.
- `loadgenerator` and `currencyservice` came back at the 300-line cap on
  essentially every sample (means 299 and 298 lines). Their learned
  exposure is about 28–30 hours, not 72, because a capped sample covers
  less than a minute of a chatty log. The floor was comparing them with
  their own level: the load generator's window was 60 against 60 expected.

## Anomalies

- **Settings applies the fence refused.** At 02:54 and again at 03:30 UTC
  on 14 November the controller logged `SentinelSettings watch ended`
  (`watch closed`) and then `could not apply SentinelSettings`: the fence
  denied a write to `configmaps/sentinel-config`. The soak was still
  running `af727ef`. `876f302`, committed at 04:49 UTC on 11 November,
  teaches that fence to allow the controller's own config maps, and it
  was not in this pod. Detection itself kept the production alpha through
  the tail, and the ledger checkpoint root was unchanged at 02:16, 03:16
  and 04:16 UTC (`size` 59), so those two refusals added no incident.
- **No missed report, no runner death.** 432 reports, one gap under the
  10-minute slot, and that gap is the clock running out. The job did not
  restart the watch.
- **The controller log is a tail.** Debug logging for 72 hours does not
  fit what `kubectl logs` still held. Per-hour error rates for the whole
  soak are not in the artifact. The incident list and the node stats are
  what cover the three days.
- **Bring-up incidents**, the four above. Outside the window, weak
  confidence (39%), resolved before the first counted report.

No flake hid a second incident. `false_alarms` is 0 from 04:26 on 11
November until 21:10 on 13 November, then 1 through the end.

## Daily cycles, and what to do about them

The load this soak actually ran is constant: ten users, one spawn a
second, for 72 hours. The node CPU agrees, within the noise of the VM,
and the one false alarm does not land on an hour that alarmed the day
before. Slice 4 held seasonal decomposition back until a soak could say
whether the ratio floor was enough for a daily cycle. On this evidence
the floor was enough, because there was no daily cycle for it to miss.

Proposed decision 123: leave the seasonal baselines unbuilt. Fit them
when a later soak, or a real workload, alarms at the same hour on
successive days, or its error rate follows a known daily shape the ratio
floor treats as a fault. Decision 36's revisit ("the soak's false alarms
come from error series; then model overdispersion and seasonality") is
one short `runtime.errors` episode that did not recur. That is too little
to retire the floor.
