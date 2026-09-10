# Protected OMP measurement baseline — 2026-09-10

Measurement-only result for HEAD `142800507b48fb9fdf39fea80f5ebc3f0d0084c4` (v0.5.0).
Production code is unchanged. The new no-change SSE regression **fails**; the full
root suite consequently fails. This report is a baseline and optimization decision,
not a production fix or a green release verdict.

## Scope and reproducibility

Added files: `omp_measurement_test.go`, `omp_benchmark_test.go`, and this report.
The preexisting deletions of `issue26_safe_mapping_test.go`,
`issue27_protected_omp_test.go`, and `issue28_live_gate_test.go` remain untouched.
No commit, push, index refresh, or production edit was performed.
The before/after `git hash-object main.go` is
`a5eadc2eb637edbd98207a2034434bd684480ce9`.

Environment: Go 1.26.0, windows/amd64, CGO enabled, Intel Core i5-12400F,
`-cpu=8` (GOMAXPROCS=8), CPA_FILTER_DEBUG unset. Benchmark runs were sequential,
without simultaneous test/profile workloads. This is a local synthetic baseline,
not Linux shared-library, network, provider, or live OMP latency measurement.

Raw logs, executable, and profiles are outside the repository:
`C:/Users/monet/AppData/Local/Temp/antigravity-cloak-baseline-adf0aee966c84ff6b259563f1b11046d`.
They are workstation-local temporary artifacts, not committed fixtures.
`baseline.txt`, `refinement.txt`, and `streams-final.txt` retain every sample;
`final-summary.json` contains the merged medians/ranges.

## Harness and boundaries

- Admission uses real `handlePluginCall(MethodRequestInterceptBefore)`, including
  its Go JSON/base64 envelope work and Protected admission. It excludes fixture
  construction, response inspection, native C dispatch, and request completion.
  One request ID is replaced repeatedly to keep route cardinality fixed.
- Exact 8/64/512 KiB bodies contain nine explicit canonical tool declarations,
  descriptions, schemas, system instructions, and tool-call/result history.
  The 64 KiB system-heavy control puts padding into the system text instead.
  Both OpenAI and Anthropic formats are covered. `n=2` exercises pinned choice count.
  These are generated representative shapes, not captured private traffic.
- Choice-count attribution runs the production `requestChoiceCount` on admitted
  canonical bytes versus reading the already-decoded root. The latter is a control,
  not an optimized production implementation or an end-to-end speed claim.
- Brand derivation isolates the current mapping-filter/normalize block from
  `rewriteProtectedBrandText` (main.go:680–693). This intentionally copied probe
  must be kept in sync if that block changes. Actual no-match rewriting at
  64 B/16 KiB measures the surrounding text work with 0/32 custom mappings.
- Stream timing calls real `streamSessionManager.processChunk` after Protected
  admission/header-init. Each operation is one complete event, including every
  fragment; it excludes admission, host-envelope dispatch, and output assembly.
  Events carry a positive brand replacement and a terminal punctuation boundary,
  so they cannot silently benchmark the broken no-change path.
- Retained session counts are actual pre-admitted, warmed entries (1/32/256).
  Concurrency uses exactly 1/8/32 goroutines with distinct request IDs, 64 retained
  entries, sequential chunks per stream, a shared start barrier, and exactly b.N
  total operations. Retained count and worker count are deliberately independent.
  Concurrent ns/op measures inverse aggregate throughput, **not** individual
  request latency, p95, or p99. The minimal matrix does not cross every retained
  count with every concurrency/fragment size.
- Preflight validation reconstructs host wire output and independently checks
  semantic text. After timing, stream benchmarks check the final emitted frame
  and session counts; concurrent benchmarks also check route counts. This keeps
  output assembly/assertion allocation outside the measured loop.
- Fresh config and managers are restored through test cleanup. No t.Parallel is
  used; workers intentionally share managers but never a request or result sink.

Existing `plugin_integration_test.go:420` fragmentation and
`plugin_integration_test.go:560` offline round-trip tests remain active.
`reverse_brand_test.go` provides lane, tool-argument, and native-termination
oracles. Their fixtures informed the same protocol shapes; their testing.T-only
helpers and transport/server setup were not put into timed loops. The small new
testing.TB helpers exercise production admission directly. No deleted test is
imported, restored, or counted as active coverage. This harness does not replace
the missing exhaustive Protected admission/lifecycle suite.

## Measurements

Values are medians; ranges are observed min–max, not confidence intervals.
Admission: 500 ms × 3. Choice/brand attribution: 1 s × 5 (refinement after noisy
initial samples). Final sweep/concurrency/SSE: 1 s × 3 after adding final-state
guards. Timing is microseconds per operation; bytes and allocations are per op.
Numbers from profiled or race-instrumented runs are not mixed into these tables.
Initial and final stream series showed host/run variability; small deltas should
be rechecked with interleaved before/after runs on the deployment OS.

### Request/admission

| Format / KiB / layout | us/op | Range us | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| OpenAI / 8 / history | 731.838 | 682.054–837.531 | 427,018 | 3,005 |
| OpenAI / 64 / history | 2,590.318 | 2,584.196–2,711.844 | 1,618,643 | 5,571 |
| OpenAI / 64 / system | 13,638.356 | 13,467.913–13,854.266 | 5,716,327 | 2,989 |
| OpenAI / 512 / history | 14,973.976 | 14,537.222–15,165.739 | 12,249,927 | 25,315 |
| Anthropic / 8 / history | 720.119 | 692.562–767.093 | 419,668 | 2,850 |
| Anthropic / 64 / history | 2,587.190 | 2,578.818–2,675.898 | 1,634,154 | 5,645 |
| Anthropic / 64 / system | 13,701.637 | 13,199.692–14,113.893 | 5,715,844 | 2,824 |
| Anthropic / 512 / history | 14,720.474 | 14,206.543–15,929.821 | 12,186,933 | 27,172 |

### Full-request reparse for n

| KiB / path | us/op | Range us | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| 8 / Reparse | 88.995 | 77.536–89.727 | 60,952 | 356 |
| 8 / ExistingRoot | 0.017 | 0.017–0.018 | 0 | 0 |
| 64 / Reparse | 639.960 | 623.717–791.370 | 435,729 | 1,306 |
| 64 / ExistingRoot | 0.018 | 0.017–0.020 | 0 | 0 |
| 512 / Reparse | 3,978.581 | 3,748.374–4,135.035 | 3,419,759 | 8,651 |
| 512 / ExistingRoot | 0.017 | 0.017–0.018 | 0 | 0 |

This supports removing redundant Protected-path decoding. The isolated 512 KiB
cost is about 27% of whole admission time; subtracting separate benchmark medians
is only an estimate, not a demonstrated optimized speedup.

### Brand-mapping derivation versus text work

| Custom mappings / operation | us/op | Range us | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| 0 / DeriveOnly | 14.758 | 14.123–15.404 | 11,408 | 123 |
| 0 / RewriteNoMatch 64 B | 21.827 | 19.834–22.313 | 15,096 | 181 |
| 0 / RewriteNoMatch 16 KiB | 1,189.997 | 1,078.921–1,229.148 | 961,657 | 181 |
| 32 / DeriveOnly | 22.902 | 21.240–23.440 | 20,496 | 124 |
| 32 / RewriteNoMatch 64 B | 33.634 | 32.008–36.692 | 26,232 | 214 |
| 32 / RewriteNoMatch 16 KiB | 1,783.224 | 1,715.785–1,868.286 | 1,495,033 | 214 |

Mapping precomputation can remove a fixed cost per text call, especially many
short fields. It cannot explain or remove most long-text scan/regex work.

### Session sweep and actual concurrency

All rows process intact OpenAI 1 KiB events.

| Retained / workers | us/op | Range us | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| 1 / 1 | 19.560 | 18.747–20.653 | 15,807 | 62 |
| 32 / 1 | 23.256 | 22.923–23.314 | 15,855 | 62 |
| 256 / 1 | 33.139 | 30.614–36.712 | 16,128 | 62 |
| 64 / 1 | 20.454 | 19.766–20.555 | 15,899 | 62 |
| 64 / 8 | 10.801 | 10.136–11.223 | 15,711 | 62 |
| 64 / 32 | 10.659 | 10.636–11.122 | 15,675 | 62 |

Retaining 256 instead of 1 increases median single-worker cost by ~69%.
Going from 8 to 32 workers buys only ~1.3% aggregate throughput here; lock
profiling corroborates contention, but these results do not identify a p99 gain
or justify sharding by themselves.

### SSE event size and fragmentation

| Format / event / fragment | us/event | Range us | B/event | allocs/event |
| --- | ---: | ---: | ---: | ---: |
| OpenAI / 1 KiB / intact | 15.884 | 14.821–16.184 | 15,841 | 62 |
| OpenAI / 1 KiB / 256 B | 20.545 | 19.727–21.165 | 18,248 | 71 |
| OpenAI / 64 KiB / intact | 802.295 | 780.396–806.449 | 1,027,569 | 74 |
| OpenAI / 64 KiB / 256 B | 23,983.747 | 23,527.902–24,611.331 | 10,158,461 | 852 |
| OpenAI / 64 KiB / 64 B stress | 93,195.467 | 92,892.167–94,554.475 | 37,248,494 | 3,153 |
| Anthropic / 1 KiB / intact | 36.553 | 35.932–36.711 | 35,257 | 157 |
| Anthropic / 1 KiB / 256 B | 41.880 | 41.337–41.914 | 37,677 | 166 |
| Anthropic / 64 KiB / intact | 1,916.054 | 1,885.142–1,987.217 | 2,465,006 | 196 |
| Anthropic / 64 KiB / 256 B | 25,104.181 | 24,976.152–25,529.708 | 11,532,261 | 965 |

The OpenAI 64 KiB/256 B case costs ~30× intact processing and ~9.9× allocated
bytes. The 64 B stress case costs ~116× intact processing. These are completed
event processing costs; network time spent waiting for fragments is excluded.

## Profile evidence

Six targeted 3-second profiles were rerun against the final harness.
Percentages below are shares of each profile's sampled CPU or cumulative mutex
delay. Cumulative call-tree values overlap and must not be added.

| Profile | Evidence | Interpretation |
| --- | --- | --- |
| request512.cpu | requestChoiceCount 17.53% cumulative CPU; handleProtectedAGY 61.94%. JSON appendCompact 12.85% flat; Decoder.readValue 11.69% flat. | Redundant n decode is material; envelope/canonical JSON work also remains. |
| system64.cpu | rewriteProtectedBrand 61.02% cumulative; replaceToolNamesInText 38.64%; rewriteProtectedBrandText 22.37%; strings.ToLower 18.81% flat / 21.19% cumulative. | Long system prompts are dominated by text scans and tool-reference regex work, beyond mapping derivation. |
| anthropic1.cpu | safeUnmarshal 33.45% cumulative; sseAnthropicTerminalKind 27.47%; sseContainsAnthropicMessageStop 18.77%; reverseBrandSSE 24.74%. | Repeated event decoding/terminal checks merit a later shared parsed-event seam. |
| fragment64k.cpu | LastIndexRabinKarp 57.68% flat / 58.05% cumulative. | Repeated backward boundary scanning is the primary CPU hotspot in this case. |
| workers1.mutex | Total cumulative delay 32.79 ms, mostly runtime locks; processChunk 0.81 ms cumulative. | No comparable application mutex bottleneck with one worker. |
| workers32.mutex | Total cumulative delay 100.62 s; Mutex.Unlock 97.44 s flat (96.84%); processChunk 97.88 s cumulative. | Session-manager serialization matters under actual concurrency. |

The workers32 source listing attributes 67.29 s of cumulative delay to
main.go:2488 (unlock after sweep/lookup), and 30.05 s to main.go:2535 (unlock
after tail combination/split). Mutex profiles charge waiting to unlock stacks;
these are aggregate waiter-seconds, **not** critical-section duration or
per-request latency. The sweep itself also obtains route authority while holding
the stream lock (main.go:2400). CPU profile runtime.lock2/unlock2 samples alone
must not be misreported as this application mutex.

## Discovered no-change SSE behavior

`TestOMPMeasurementFragmentedNoChange` at omp_measurement_test.go:92 drives the
real request and stream ABI handlers. It compares downstream wire bytes, applying
SDK host semantics: DropChunk suppresses input, nonempty Body replaces input, and
empty Body forwards the current input. These semantics are documented in
CLIProxyAPI v7.2.143 sdk/pluginapi/types.go:1124–1128.

Both intact controls pass. With the event split before “world” and before its
final delimiter, both fragmented cases fail:

| Framing | Expected | Actual downstream |
| --- | --- | --- |
| LF | Complete 87-byte event | Only 2 bytes, LF LF |
| CRLF | Complete 89-byte event | Only 4 bytes, CRLF CRLF |

main.go:2537–2539 drops earlier incomplete chunks. When the completed buffered
event needs no replacement, main.go:2600–2601 returns an empty response.
The host therefore forwards only the last input fragment, losing the earlier
payload. This is a reproducible bug candidate at the plugin/host contract seam,
not a benchmark assertion about speed. It is intentionally left failing and
unskipped; no production remediation was attempted.

A separate fixture-development observation: a description containing
`Keep .omp/agent paths intact.` lost that relative path to brand rewriting.
The controlled benchmark fixture uses `/workspace/.omp/agent`; both fixtures
were not performance-equivalent inputs. main.go:3991's path recognizer requires
start-of-string or a slash/backslash before the dot. This additional behavior is
outside this measurement phase; path preservation in the final fixture must not
be read as proof that relative paths embedded in prose are protected.

## Ranked optimization decision and owning seams

Ranking is workload-dependent; event and request frequencies must inform a
deployment decision. The SSE correctness failure is a separate prerequisite,
not an optimization to hide inside performance changes.

1. **Fragment accumulation and boundary scan — highest measured event CPU,
   allocation, and processing-latency opportunity when large events fragment.**
   Narrow seam: processChunk's tail assembly/split (main.go:2520–2539) and
   splitSSEEvents (2694). The current growing copy plus full boundary rescans
   explains the scaling; an incremental boundary search/buffer design is worth
   a separate change. It also shortens work currently under m.mu. Preserve exact
   LF/CRLF frame ordering, multiple events plus trailing incomplete bytes,
   UTF-8 across fragments, DropChunk/empty-Body host behavior, tool/brand carries
   per lane, and terminal flush ordering. Benchmark success must include both
   changed and no-change wire oracles once the correctness bug is addressed.
2. **Avoid Protected n reparse — clearest bounded request CPU/allocation win.**
   Narrow seam: handleProtectedAGY rootMap to pinned expected (main.go:804/874),
   with requestChoiceCount/jsonIndexValue semantics (1939). Keep strict
   single-document admission, failure responses, post-transform validation,
   and canonical serialization unchanged. Preserve missing/invalid/nonintegral/
   nonpositive n defaulting to 1 and numeric range behavior; test them before
   using an already-decoded number representation. Active reverse mappings must
   still contain only transformed declarations, keyed by exact namespace,
   with collision admission comparing final base identities. No global reverse
   table may replace per-request authority. n stays pinned to the admitted
   request; later config/request metadata cannot overwrite it.
3. **Session sweep frequency/critical-section scope — first concurrency target.**
   Narrow seam: cleanupStaleLocked and processChunk's two manager-lock regions
   (2400, 2485, 2508). Supported by retained-count scaling and application mutex
   profiles, primarily contention/aggregate throughput rather than allocations.
   Preserve durable route ownership until request.complete; [DONE] cleans only
   disposable sessions. Rehydration is permitted only from pinned route state
   before payload activity; never recreate lost active payload/carry state.
   Protected live tails, lane progress, and brand carries cannot expire through
   a generic TTL sweep. Preserve RequestID isolation, bypass durability, lock
   ordering, and terminal disposition under concurrency. Measure a bounded sweep
   change before introducing sharding.
4. **Long system-text rewrite scans, then Anthropic repeated event decoding.**
   Both are real CPU/allocation targets. Use rewriteProtectedBrandText/
   replaceToolNamesInText and reverseBrandSingleSSEEvent/terminal classification
   respectively. Preserve brand alias terminal outputs, custom mapping priority,
   literal paths, tool-reference boundaries, tool arguments, lane isolation,
   and native message_stop/content_block_stop semantics. The system-heavy
   control shows why a blanket “JSON is everything” conclusion would be wrong.
5. **Brand-mapping precomputation — smaller, bounded allocation improvement.**
   Precompute only immutable config-derived mappings at config application,
   preserving normalization/order, reconfigure visibility, OMP terminal policy,
   and per-request reverse authority. The derive-only probe supports a fixed
   ~15–23 us/text opportunity, not a fix for the ~1.2–1.8 ms long-text case.

Splitting main.go, moving handlers into files, renaming helpers, or introducing
generic interfaces are maintainability work; this baseline assigns them no
performance benefit. Defer generic caching frameworks, global Protected reverse
caches, map sharding, buffer pools, a custom JSON parser, background cleanup
workers, and a full Cartesian benchmark matrix under YAGNI until a narrower
measured change demonstrates need. Debug-body I/O was disabled; this report does
not benchmark or recommend optimizing enabled debug logging.

## Executed validation and benchmark commands

Commands ran from the repository root using Git Bash. In the following blocks,
D abbreviates the exact temporary directory above; substituting D reproduces
the executed command arguments and log destinations.

```bash
D='C:/Users/monet/AppData/Local/Temp/antigravity-cloak-baseline-adf0aee966c84ff6b259563f1b11046d'
```

| Command | Exit | Result |
| --- | ---: | --- |
| `env -u CPA_FILTER_DEBUG go test -run '^TestOMPMeasurementFragmentedNoChange$' -count=1 -v .` | 1 | LF/CRLF fragmented fail; intact pass. Saved regression.log. |
| `env -u CPA_FILTER_DEBUG go test -race -run '^TestOMPMeasurementFixtures$' -bench '^BenchmarkOMPConcurrentStreams$' -benchtime=100x -count=1 -cpu=8 .` | 0 | Fixtures and actual 1/8/32 workers pass race instrumentation; race.log. |
| `env -u CPA_FILTER_DEBUG go test ./...` | 1 | Only the new no-change regression subtests fail; all.log. |
| `go test ./.github/scripts` | 0 | Pass (cached); scripts.log. |
| `go vet ./...` | 0 | vet.log empty. |
| `git diff --check` | 0 | No tracked-diff whitespace errors. |

Each saved validation has a matching .exit file. No regression is skipped to
make the full suite green.

```bash
# Initial complete smoke: exit 0
env -u CPA_FILTER_DEBUG go test -run '^$' -bench '^BenchmarkOMP' -benchtime=1x -count=1 -cpu=8 -benchmem .

# Initial complete baseline: exit 0
env -u CPA_FILTER_DEBUG go test -run '^$' -bench '^BenchmarkOMP' -benchtime=500ms -count=3 -cpu=8 -benchmem . > "$D/baseline.txt" 2>&1

# Attribution refinement: exit 0
env -u CPA_FILTER_DEBUG go test -run '^$' -bench '^BenchmarkOMP(ChoiceCount|BrandDerivation)$' -benchtime=1s -count=5 -cpu=8 -benchmem . > "$D/refinement.txt" 2>&1

# Final-state guards and fixtures: exit 0
gofmt -w omp_benchmark_test.go && env -u CPA_FILTER_DEBUG go test -run '^TestOMPMeasurementFixtures$' -bench '^BenchmarkOMP(SessionSweep|ConcurrentStreams|SSE)$' -benchtime=1x -count=1 -cpu=8 -benchmem .

# Final guarded stream baseline: exit 0
env -u CPA_FILTER_DEBUG go test -run '^$' -bench '^BenchmarkOMP(SessionSweep|ConcurrentStreams|SSE)$' -benchtime=1s -count=3 -cpu=8 -benchmem . > "$D/streams-final.txt" 2>&1
```

Profile commands (each exit 0, final rerun supersedes initial profile files):

```bash
env -u CPA_FILTER_DEBUG go test -run '^$' -bench '^BenchmarkOMPAdmission$/^openai$/^512KiB$/^history$' -benchtime=3s -count=1 -cpu=8 -benchmem -cpuprofile="$D/request512.cpu" -o "$D/omp.test.exe" . > "$D/request512.txt" 2>&1
env -u CPA_FILTER_DEBUG go test -run '^$' -bench '^BenchmarkOMPAdmission$/^openai$/^64KiB$/^system$' -benchtime=3s -count=1 -cpu=8 -benchmem -cpuprofile="$D/system64.cpu" -o "$D/omp.test.exe" . > "$D/system64.txt" 2>&1
env -u CPA_FILTER_DEBUG go test -run '^$' -bench '^BenchmarkOMPSSE$/^anthropic$/^1KiB$/^fragment1024$' -benchtime=3s -count=1 -cpu=8 -benchmem -cpuprofile="$D/anthropic1.cpu" -o "$D/omp.test.exe" . > "$D/anthropic1.txt" 2>&1
env -u CPA_FILTER_DEBUG go test -run '^$' -bench '^BenchmarkOMPSSE$/^openai$/^64KiB$/^fragment256$' -benchtime=3s -count=1 -cpu=8 -benchmem -cpuprofile="$D/fragment64k.cpu" -o "$D/omp.test.exe" . > "$D/fragment64k.txt" 2>&1
env -u CPA_FILTER_DEBUG go test -run '^$' -bench '^BenchmarkOMPConcurrentStreams$/^workers32$' -benchtime=3s -count=1 -cpu=8 -benchmem -mutexprofile="$D/workers32.mutex" -mutexprofilefraction=1 -o "$D/omp.test.exe" . > "$D/workers32.txt" 2>&1
env -u CPA_FILTER_DEBUG go test -run '^$' -bench '^BenchmarkOMPConcurrentStreams$/^workers1$' -benchtime=3s -count=1 -cpu=8 -benchmem -mutexprofile="$D/workers1.mutex" -mutexprofilefraction=1 -o "$D/omp.test.exe" . > "$D/workers1.txt" 2>&1
```

Inspection commands, each exit 0 (P is each of request512.cpu, system64.cpu,
anthropic1.cpu, fragment64k.cpu, workers1.mutex, workers32.mutex):

```bash
go tool pprof -top -nodecount=15 "$D/omp.test.exe" "$D/$P"
go tool pprof -list 'processChunk' "$D/omp.test.exe" "$D/workers32.mutex"
go tool pprof -top -cum -nodecount=150 "$D/omp.test.exe" "$D/request512.cpu" | rg 'requestChoiceCount|handleProtectedAGY'
go tool pprof -top -cum -nodecount=150 "$D/omp.test.exe" "$D/system64.cpu" | rg 'rewriteProtected|rewriteTool|replace'
go tool pprof -top -cum -nodecount=150 "$D/omp.test.exe" "$D/anthropic1.cpu" | rg 'safeUnmarshal|json.*Decode|reverseBrand|Anthropic|uncloakStreamChunk'
```

Development checks: `gofmt -w omp_measurement_test.go && go test -run
'^TestOMPMeasurementFragmentedNoChange$' -count=1 -v .` exited 1 for the same
reproduction. `gofmt -w omp_benchmark_test.go && go test -run
'^TestOMPMeasurementFixtures$' -count=1 -v .` initially exited 1 on the relative
path fixture; a diagnostic repeat of that test also exited 1. After the controlled
absolute-path fixture adjustment, the formatting/test command exited 0.
`git diff --no-index --check -- /dev/null omp_benchmark_test.go` and the same
command for omp_measurement_test.go exited 1 because files differ from /dev/null;
only LF-to-CRLF warnings were printed, no whitespace-error diagnostics.
The report's first no-index whitespace check exited 3 for an extra blank line
at EOF; that formatting error was removed before final validation.

## Remaining acceptance boundary

A later performance implementation must first make the no-change wire regression
pass through a separately reviewed correctness change, preserve Protected
admission/lifecycle invariants, then compare these same cases with interleaved
baseline/candidate runs. Add only the negative/terminal cases needed by the seam
being changed. This phase establishes evidence; it does not authorize production
remediation, restore missing coverage, or prove live deployment readiness.
