// Command loadgen offers a reproducible, open-model trading workload to a
// running cexd and reports what the client saw AND what the server measured
// about itself, stage by stage (week 7).
//
//	loadgen tokens loadtest/workloads/step.json        # CEX_API_TOKENS for the server
//	loadgen run -workload loadtest/workloads/step.json -out evidence/load/<name>
//
// The server must be started with the printed tokens and a rate limit high
// enough for the workload (CEX_RATE_PER_SECOND), ideally on a fresh data
// directory. See loadtest/README.md for the method and how to read results.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if err := cli(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func cli(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: loadgen tokens <workload.json> | loadgen run [flags]")
	}
	switch args[0] {
	case "tokens":
		if len(args) != 2 {
			return errors.New("usage: loadgen tokens <workload.json>")
		}
		w, err := LoadWorkload(args[1])
		if err != nil {
			return err
		}
		s, err := tokensJSON(w.Users)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, s)
		return err
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		var o options
		fs.StringVar(&o.workload, "workload", "", "workload definition (JSON); required")
		fs.StringVar(&o.url, "url", "http://127.0.0.1:8080", "cexd public API base URL")
		fs.StringVar(&o.admin, "admin", "http://127.0.0.1:6060", "cexd admin base URL for /metrics and pprof; empty = client-side results only")
		fs.StringVar(&o.out, "out", "", "directory for the results; required")
		fs.StringVar(&o.label, "label", "", "short label for this run, e.g. \"batch=64\"")
		fs.StringVar(&o.note, "note", "", "machine and storage description recorded with the results")
		fs.IntVar(&o.profileStage, "profile-stage", 0, "capture a CPU profile during this stage (1-based; 0 = none)")
		fs.IntVar(&o.slowViewers, "slow-viewers", 0, "WebSocket viewers that read only one message every -slow-read")
		fs.DurationVar(&o.slowRead, "slow-read", 5*time.Second, "how often each slow viewer reads")
		fs.BoolVar(&o.sameMachine, "same-machine", true, "the load generator runs on the server's machine (recorded: it competes for CPU)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if o.workload == "" || o.out == "" {
			return errors.New("run: -workload and -out are required")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return run(ctx, o, stdout)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

type options struct {
	workload, url, admin, out, label, note string
	profileStage, slowViewers              int
	slowRead                               time.Duration
	sameMachine                            bool
}

func run(ctx context.Context, o options, stdout io.Writer) error {
	w, err := LoadWorkload(o.workload)
	if err != nil {
		return err
	}
	if o.profileStage < 0 || o.profileStage > len(w.Stages) {
		return fmt.Errorf("-profile-stage must be 0..%d", len(w.Stages))
	}
	if o.profileStage > 0 && o.admin == "" {
		return errors.New("-profile-stage needs -admin")
	}
	runID := time.Now().UTC().Format("20060102T150405")
	c := newClient(o.url, w.MaxInFlight, w.Timeout.Duration, w.Users, runID)
	r := newRunner(w, c)

	// Server metrics: optional, but without them the report cannot say
	// WHERE the time went.
	var sc *scraper
	var first *scrape
	if o.admin != "" {
		sc = &scraper{url: strings.TrimRight(o.admin, "/") + "/metrics", http: &http.Client{Timeout: 5 * time.Second}}
		s, err := sc.get(ctx)
		if err != nil {
			return fmt.Errorf("reading server metrics (pass -admin \"\" to skip): %w", err)
		}
		first = &s
	}

	fmt.Fprintf(os.Stderr, "loadgen: funding %d users...\n", w.Users)
	if err := c.fund(ctx, w); err != nil {
		return err
	}

	// The 1-second sampler: a timeline of the server's state during the run.
	var stage atomic.Int64
	stage.Store(-1)
	var (
		mu       sync.Mutex
		during   = map[int][]scrape{}
		timeline []timelineRow
	)
	samplerCtx, stopSampler := context.WithCancel(ctx)
	samplerDone := make(chan struct{})
	r.start = time.Now()
	go func() {
		defer close(samplerDone)
		if sc == nil {
			return
		}
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-samplerCtx.Done():
				return
			case <-t.C:
				s, err := sc.get(samplerCtx)
				if err != nil {
					continue // a missed sample is a gap in the timeline, not a failed run
				}
				idx := int(stage.Load())
				mu.Lock()
				during[idx] = append(during[idx], s)
				timeline = append(timeline, rowFrom(s.at.Sub(r.start).Seconds(), idx, s))
				mu.Unlock()
			}
		}
	}()

	var viewers *slowViewers
	if o.slowViewers > 0 {
		viewers = startSlowViewers(samplerCtx, o.slowViewers, streamURL(o.url), o.slowRead)
	}

	// boundary scrapes: the server's counters at the start of each stage
	// and at the very end. Stage i's server stats are boundary[i+1] minus
	// boundary[i]. Requests in flight across a boundary are counted in the
	// stage in which the server finished them.
	type boundary struct {
		s  scrape
		ok bool
	}
	var bounds []boundary
	mark := func() {
		if sc == nil {
			bounds = append(bounds, boundary{})
			return
		}
		s, err := sc.get(ctx)
		bounds = append(bounds, boundary{s, err == nil})
	}

	var profile string
	var profileDone chan struct{}
	plan := w.Stages
	if w.Warmup.Duration > 0 {
		fmt.Fprintf(os.Stderr, "loadgen: warmup %s at %g/s\n", w.Warmup, w.Stages[0].Rate)
		mark()
		stage.Store(-1)
		r.stage(ctx, -1, w.Stages[0].Rate, w.Warmup.Duration)
	}
	for i, st := range plan {
		fmt.Fprintf(os.Stderr, "loadgen: stage %d/%d: %g/s for %s\n", i+1, len(plan), st.Rate, st.Duration)
		mark()
		stage.Store(int64(i))
		if o.profileStage == i+1 {
			profile = "cpu.pprof"
			profileDone = make(chan struct{})
			secs := max(1, min(30, int(st.Duration.Seconds())-2))
			go func() {
				defer close(profileDone)
				if err := fetchProfile(ctx, strings.TrimRight(o.admin, "/"), secs, filepath.Join(o.out, profile)); err != nil {
					fmt.Fprintln(os.Stderr, "loadgen: CPU profile failed:", err)
				}
			}()
		}
		r.stage(ctx, i, st.Rate, st.Duration.Duration)
	}
	r.wait()
	mark()
	if profileDone != nil {
		<-profileDone
	}
	stopSampler()
	<-samplerDone
	if viewers != nil {
		viewers.wg.Wait()
	}
	if ctx.Err() != nil {
		return errors.New("interrupted")
	}

	res := Results{Label: o.label, Environment: newEnvironment(o.note, o.sameMachine, first), Workload: w, Profile: profile}
	b := 0 // index into bounds
	attach := func(s *StageStats, idx int) {
		if sc != nil && bounds[b].ok && bounds[b+1].ok {
			s.Server = serverStats(bounds[b].s, bounds[b+1].s, during[idx])
		}
		b++
	}
	if w.Warmup.Duration > 0 {
		ws := summarize("warmup", Stage{Rate: w.Stages[0].Rate, Duration: w.Warmup}, r.stageSamples(-1))
		attach(&ws, -1)
		res.Warmup = &ws
	}
	for i, st := range plan {
		s := summarize(fmt.Sprintf("%d: %g/s", i+1, st.Rate), st, r.stageSamples(i))
		attach(&s, i)
		res.Stages = append(res.Stages, s)
	}
	if viewers != nil {
		res.Viewers = &ViewerStats{Viewers: o.slowViewers, ReadEvery: o.slowRead.String(), Connects: viewers.connects.Load(), Drops: viewers.drops.Load()}
	}
	var last []byte
	if sc != nil && bounds[len(bounds)-1].ok {
		last = bounds[len(bounds)-1].s.raw
	}
	r.mu.Lock()
	samples := r.samples
	r.mu.Unlock()
	if err := writeAll(o.out, res, samples, timeline, last); err != nil {
		return err
	}
	_, err = io.WriteString(stdout, markdown(res))
	return err
}

// fetchProfile saves a CPU profile taken by the server while the stage runs.
func fetchProfile(ctx context.Context, admin string, secs int, path string) error {
	time.Sleep(time.Second) // let the stage's arrival rate settle first
	client := &http.Client{Timeout: time.Duration(secs+15) * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/debug/pprof/profile?seconds=%d", admin, secs), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
