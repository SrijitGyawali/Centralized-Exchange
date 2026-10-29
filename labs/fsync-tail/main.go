// Command fsync-tail measures the DISTRIBUTION of fsync latency for small
// appends, three ways, to test one hypothesis from the load tests:
//
//	Rare fsyncs of 100 ms - 1 s stall the engine. Is that because the
//	journal GROWS on every append, so each fsync must also flush file-system
//	metadata (the new size and allocation)?
//
// Modes (each writes -n records of -size bytes, one fsync per record):
//
//	grow      append to a new, empty file: the journal today
//	truncate  extend the file to its final size first (SetEndOfFile, no data
//	          written), then overwrite from the start
//	zerofill  write the whole file with zeros and fsync once first, then
//	          overwrite from the start: the file's size AND its valid data
//	          already cover every later write
//
//	go run ./labs/fsync-tail -n 10000 -rounds 2
//
// See NOTES.md for the prediction, the results and what they decided.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/clock"
)

func main() {
	n := flag.Int("n", 10_000, "records (and fsyncs) per mode")
	size := flag.Int("size", 128, "bytes per record (one small journal batch)")
	rounds := flag.Int("rounds", 2, "repeat every mode this many times, interleaved")
	dir := flag.String("dir", "", "directory on the disk to test (default: a temp dir)")
	modes := flag.String("modes", "grow,truncate,zerofill", "comma-separated modes to run")
	flag.Parse()
	if *dir == "" {
		d, err := os.MkdirTemp("", "fsync-tail-")
		check(err)
		defer os.RemoveAll(d)
		*dir = d
	}
	fmt.Printf("%d records of %d bytes per run, one fsync each, dir %s\n\n", *n, *size, *dir)
	fmt.Printf("%-9s %5s %8s %8s %8s %8s %9s %7s %7s\n", "mode", "round", "p50", "p90", "p99", "p99.9", "max", ">10ms", ">100ms")
	for r := 1; r <= *rounds; r++ {
		for _, mode := range strings.Split(*modes, ",") {
			lat := run(filepath.Join(*dir, fmt.Sprintf("%s-%d.dat", mode, r)), mode, *n, *size)
			report(mode, r, lat)
		}
	}
}

func run(path, mode string, n, size int) []time.Duration {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	check(err)
	defer func() { check(f.Close()); check(os.Remove(path)) }()
	total := int64(n * size)
	switch mode {
	case "truncate":
		check(f.Truncate(total)) // size set, no data written
		check(f.Sync())
	case "zerofill":
		zeros := make([]byte, 1<<20)
		for written := int64(0); written < total; written += int64(len(zeros)) {
			_, err := f.Write(zeros[:min(int64(len(zeros)), total-written)])
			check(err)
		}
		check(f.Sync())
		_, err := f.Seek(0, 0)
		check(err)
	}
	rec := make([]byte, size)
	for i := range rec {
		rec[i] = byte('a' + i%26)
	}
	lat := make([]time.Duration, 0, n)
	for range n {
		_, err := f.Write(rec)
		check(err)
		start := clock.Now()
		check(f.Sync())
		lat = append(lat, clock.Since(start))
	}
	return lat
}

func report(mode string, round int, lat []time.Duration) {
	slices.Sort(lat)
	at := func(q float64) string {
		return fmt.Sprintf("%.3f", float64(lat[min(len(lat)-1, int(q*float64(len(lat))))])/1e6)
	}
	over := func(d time.Duration) int {
		i, _ := slices.BinarySearch(lat, d)
		return len(lat) - i
	}
	fmt.Printf("%-9s %5d %8s %8s %8s %8s %9s %7d %7d\n", mode, round, at(.5), at(.9), at(.99), at(.999),
		fmt.Sprintf("%.3f", float64(lat[len(lat)-1])/1e6), over(10*time.Millisecond), over(100*time.Millisecond))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
