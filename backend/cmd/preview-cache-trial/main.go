//go:build linux

// preview-cache-trial inventories a dedicated preview cache without starting the server.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cachetrial"
)

func main() {
	var opt cachetrial.Options
	flag.StringVar(&opt.CacheDir, "cache-dir", "", "parent of the dedicated diskcache directory")
	flag.StringVar(&opt.Snapshot, "snapshot", "", "sealed standalone SQLite backup in DELETE journal mode")
	flag.StringVar(&opt.ExpectSnapshot, "expect-snapshot", "", "snapshot_id from the previous report when resuming")
	flag.Int64Var(&opt.AfterResult, "after-result", 0, "resume after this SQLite result rowid in the same backup")
	flag.StringVar(&opt.AfterCache, "after-cache", "", "resume using next_cache from the same quiescent cache")
	flag.IntVar(&opt.MaxEntries, "max-entries", 2048, "maximum visits per section (1-20000)")
	flag.Int64Var(&opt.MaxBytes, "max-bytes", 256<<20, "logical-byte ceiling per section")
	flag.DurationVar(&opt.MaxTime, "max-time", 3*time.Second, "cooperative time ceiling (maximum 30s)")
	flag.IntVar(&opt.Samples, "samples", 12, "maximum anonymized samples per section (0-50)")
	flag.StringVar(&opt.FixtureEvidence, "fixture-evidence", "", "assertions for synthetic fixture runs only")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, opt)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, opt cachetrial.Options) int {
	report, err := cachetrial.Run(ctx, opt)
	if err != nil {
		if ctx.Err() != nil {
			return 130
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, "cannot write trial report")
		return 1
	}
	switch report.Stop {
	case "":
		return 0
	case "canceled":
		return 130
	default:
		return 2
	}
}
