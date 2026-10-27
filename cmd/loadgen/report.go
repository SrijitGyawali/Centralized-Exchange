package main

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Environment records where a result came from. A latency number without
// its machine, commit and server settings cannot be compared with anything.
type Environment struct {
	StartedAt        string  `json:"started_at"`
	Note             string  `json:"note"` // -note: hardware, storage, anything not detectable
	OS               string  `json:"os"`
	Arch             string  `json:"arch"`
	CPUs             int     `json:"logical_cpus"`
	LoadgenGo        string  `json:"loadgen_go"`
	GitCommit        string  `json:"git_commit"`
	ServerVersion    string  `json:"server_version"`
	ServerGo         string  `json:"server_go"`
	ServerBatchLimit float64 `json:"server_batch_limit"`
	ServerQueueCap   float64 `json:"server_queue_capacity"`
	SameMachine      bool    `json:"loadgen_on_server_machine"`
}

// ViewerStats summarizes the slow WebSocket viewers.
type ViewerStats struct {
	Viewers   int    `json:"viewers"`
	ReadEvery string `json:"read_every"`
	Connects  int64  `json:"connects"`
	Drops     int64  `json:"drops_observed"`
}

// Results is everything written to results.json.
type Results struct {
	Label       string       `json:"label"`
	Environment Environment  `json:"environment"`
	Workload    Workload     `json:"workload"`
	Warmup      *StageStats  `json:"warmup,omitempty"`
	Stages      []StageStats `json:"stages"`
	Viewers     *ViewerStats `json:"slow_viewers,omitempty"`
	Profile     string       `json:"cpu_profile,omitempty"`
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	commit := strings.TrimSpace(string(out))
	if st, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output(); err == nil && len(st) > 0 {
		commit += "-dirty" // uncommitted changes: the result is not reproducible from the commit alone
	}
	return commit
}

func newEnvironment(note string, sameMachine bool, first *scrape) Environment {
	env := Environment{
		StartedAt: time.Now().UTC().Format(time.RFC3339), Note: note,
		OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(),
		LoadgenGo: runtime.Version(), GitCommit: gitCommit(), SameMachine: sameMachine,
	}
	if first != nil {
		env.ServerVersion = first.label("cex_build_info", "version")
		env.ServerGo = first.label("cex_build_info", "go_version")
		env.ServerBatchLimit = first.value("cex_engine_batch_limit", nil)
		env.ServerQueueCap = first.value("cex_engine_queue_capacity", nil)
	}
	return env
}

// writeAll writes every output file into dir.
func writeAll(dir string, res Results, samples []sample, timeline []timelineRow, lastScrape []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(markdown(res)), 0o644); err != nil {
		return err
	}
	if err := writeSamples(filepath.Join(dir, "requests.csv.gz"), samples); err != nil {
		return err
	}
	if err := writeTimeline(filepath.Join(dir, "timeline.csv"), timeline); err != nil {
		return err
	}
	if lastScrape != nil {
		return os.WriteFile(filepath.Join(dir, "metrics-end.prom"), lastScrape, 0o644)
	}
	return nil
}

// writeSamples writes one CSV row per arrival, gzip-compressed. These are
// the raw results every number in the report is computed from.
func writeSamples(path string, samples []sample) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := gzip.NewWriter(f)
	w := csv.NewWriter(zw)
	_ = w.Write([]string{"stage", "kind", "scheduled_ms", "latency_ms", "status", "dropped"})
	sorted := slices.Clone(samples)
	slices.SortFunc(sorted, func(a, b sample) int { return int(a.Offset - b.Offset) })
	for _, s := range sorted {
		_ = w.Write([]string{
			strconv.Itoa(s.Stage), s.Kind.String(),
			strconv.FormatFloat(ms(s.Offset), 'f', 3, 64), strconv.FormatFloat(ms(s.Latency), 'f', 3, 64),
			strconv.Itoa(s.Status), strconv.FormatBool(s.Dropped),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}

// timelineRow is one per-second sample of the server's gauges and counters.
type timelineRow struct {
	T                                                            float64 // seconds since the run started
	Stage                                                        int
	AppliedSeq, QueueLen, Lag, Subscribers, SlowDrops, RSSMiB    float64
	Goroutines, CPUSeconds, Overloaded, ProjectionHealthy, Fills float64
}

func rowFrom(t float64, stage int, s scrape) timelineRow {
	return timelineRow{
		T: t, Stage: stage,
		AppliedSeq: s.value("cex_engine_applied_seq", nil), QueueLen: s.value("cex_engine_queue_length", nil),
		Lag: s.value("cex_projection_lag_commands", nil), Subscribers: s.value("cex_marketdata_subscribers", nil),
		SlowDrops: s.value("cex_marketdata_slow_drops_total", nil), RSSMiB: s.value("process_resident_memory_bytes", nil) / (1 << 20),
		Goroutines: s.value("go_goroutines", nil), CPUSeconds: s.value("process_cpu_seconds_total", nil),
		Overloaded:        s.value("cex_engine_refused_total", map[string]string{"reason": "overloaded"}),
		ProjectionHealthy: s.value("cex_projection_healthy", nil), Fills: s.value("cex_engine_fills_total", nil),
	}
}

func writeTimeline(path string, rows []timelineRow) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"t_s", "stage", "applied_seq", "queue_length", "projection_lag", "projection_healthy",
		"ws_subscribers", "ws_slow_drops_total", "rss_mib", "goroutines", "cpu_seconds_total", "refused_overloaded_total", "fills_total"})
	g := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	for _, r := range rows {
		_ = w.Write([]string{strconv.FormatFloat(r.T, 'f', 1, 64), strconv.Itoa(r.Stage), g(r.AppliedSeq), g(r.QueueLen), g(r.Lag),
			g(r.ProjectionHealthy), g(r.Subscribers), g(r.SlowDrops), strconv.FormatFloat(r.RSSMiB, 'f', 1, 64), g(r.Goroutines),
			strconv.FormatFloat(r.CPUSeconds, 'f', 3, 64), g(r.Overloaded), g(r.Fills)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return f.Close()
}

// markdown renders report.md: the human-readable view of results.json.
func markdown(r Results) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w, e := r.Workload, r.Environment
	p("# Load test: %s", w.Name)
	if r.Label != "" {
		p(" (%s)", r.Label)
	}
	p("\n\n%s\n\n", w.Description)
	p("| | |\n|---|---|\n")
	p("| started (UTC) | %s |\n| git commit | `%s` |\n| machine | %s, %s/%s, %d logical CPUs |\n", e.StartedAt, e.GitCommit, e.Note, e.OS, e.Arch, e.CPUs)
	p("| server | %s (%s), batch limit %g, queue capacity %g |\n", e.ServerVersion, e.ServerGo, e.ServerBatchLimit, e.ServerQueueCap)
	p("| load generator | %s, same machine as the server: %v |\n", e.LoadgenGo, e.SameMachine)
	p("| workload | %d users, mix rest/cross/cancel = %d/%d/%d, seed %d, max in flight %d, timeout %s |\n\n",
		w.Users, w.Mix.Rest, w.Mix.Cross, w.Mix.Cancel, w.Seed, w.MaxInFlight, w.Timeout)

	stages := r.Stages
	if r.Warmup != nil {
		stages = append([]StageStats{*r.Warmup}, stages...)
	}
	p("## Client view: latency from the scheduled arrival (exact percentiles, ms)\n\n")
	p("Answered = 200/404/409/422 (the engine decided). Errors = 429, 5xx, no response. Dropped = never sent (max in flight reached).\n\n")
	p("| stage | offered/s | answered/s | dropped | errors | error rate | p50 | p95 | p99 | p99.9 | max |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range stages {
		p("| %s | %g | %g | %d | %d | %.2f%% | %.2f | %.2f | %.2f | %.2f | %.2f |\n", s.Name, s.OfferedRate, s.CompletedRate, s.Dropped, s.Errors, 100*s.ErrorRate,
			s.Latency.P50, s.Latency.P95, s.Latency.P99, s.Latency.P999, s.Latency.Max)
	}
	p("\n## Status codes\n\n| stage | 200 | 404 | 409 | 422 | 429 | 503 | 504 | no response |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range stages {
		c := s.ByStatus
		p("| %s | %d | %d | %d | %d | %d | %d | %d | %d |\n", s.Name, c["200"], c["404"], c["409"], c["422"], c["429"], c["503"], c["504"], c["error"])
	}
	if len(stages) > 0 && stages[0].Server != nil {
		p("\n## Server view: where the time went (histogram estimates, ms)\n\n")
		p("Each level contains the next: HTTP ⊃ engine total ⊃ queue wait + durable + apply. HTTP covers answered order requests only (refusals excluded). ")
		p("Engine rows cover every applied command (refused requests never reach the engine). Durable is per batch; batch = mean commands per fsync.\n\n")
		p("| stage | HTTP p50 / p99 | engine total p50 / p99 | queue wait p50 / p99 | durable p50 / p99 | batch | apply p50 / p99 | fsync p50 / p99 |\n|---|---|---|---|---|---|---|---|\n")
		for _, s := range stages {
			v := s.Server
			p("| %s | %s | %s | %s | %s | %g | %s | %s |\n", s.Name, pair(v.HTTP), pair(v.EngineTotal), pair(v.QueueWait), pair(v.Durable), v.BatchMean, pair(v.Apply), pair(v.Fsync))
		}
		p("\n## Saturation\n\n| stage | CPU cores used | max queue / capacity | refused (overloaded) | max goroutines | max RSS MiB | max projection lag | WS slow drops |\n|---|---|---|---|---|---|---|---|\n")
		for _, s := range stages {
			v := s.Server
			p("| %s | %g | %g / %g | %g | %g | %g | %g | %g |\n", s.Name, v.CPUCores, v.MaxQueueLen, v.QueueCap, v.Overloaded, v.MaxGorout, v.MaxRSSMiB, v.MaxLag, v.SlowDrops)
		}
	}
	if r.Viewers != nil {
		p("\n## Slow WebSocket viewers\n\n%d viewers reading one message every %s: %d connections, %d disconnects observed.\n",
			r.Viewers.Viewers, r.Viewers.ReadEvery, r.Viewers.Connects, r.Viewers.Drops)
	}
	p("\n## Raw files\n\n- `results.json`: every number above, plus the workload\n- `requests.csv.gz`: one row per arrival (stage, kind, scheduled time, latency, status, dropped)\n")
	p("- `timeline.csv`: the server's gauges and counters, sampled every second\n- `metrics-end.prom`: the server's full /metrics at the end of the run\n")
	if r.Profile != "" {
		p("- `%s`: CPU profile (`go tool pprof -top %s`)\n", r.Profile, r.Profile)
	}
	return b.String()
}

func pair(h HistSummary) string {
	if h.Count == 0 {
		return "–"
	}
	return fmt.Sprintf("%.3f / %.3f", h.P50Ms, h.P99Ms)
}
