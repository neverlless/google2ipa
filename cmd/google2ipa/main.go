// Command google2ipa syncs Google Workspace users into FreeIPA / Red Hat IdM.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neverlless/google2ipa/internal/config"
	"github.com/neverlless/google2ipa/internal/google"
	"github.com/neverlless/google2ipa/internal/ipa"
	"github.com/neverlless/google2ipa/internal/notify"
	"github.com/neverlless/google2ipa/internal/reconcile"
)

var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	cfgPath := flag.String("config", "config.yaml", "path to the configuration file")
	dryRun := flag.Bool("dry-run", false, "print the planned changes without applying them")
	interval := flag.Duration("interval", 0, "repeat the sync every interval (e.g. 30m); 0 runs once")
	timeout := flag.Duration("timeout", 30*time.Minute, "abort a single pass that runs longer than this")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return 0
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	log := newLogger(cfg.Log)
	mailer, err := notify.New(cfg.Notify, cfg.FreeIPA.URL)
	if err != nil {
		log.Error(err.Error())
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// failed reports a pass that could not start, including to the admin.
	failed := func(err error) bool {
		log.Error(err.Error())
		if !*dryRun {
			if err := mailer.Summary(reconcile.Report{Errors: []string{err.Error()}}); err != nil {
				log.Error("summary mail", "err", err)
			}
		}
		return false
	}
	once := func() bool {
		pctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		src, err := google.New(pctx, cfg.Google, cfg.Sync.GoogleGroups())
		if err != nil {
			return failed(err)
		}
		tgt, err := ipa.Connect(pctx, cfg.FreeIPA, cfg.Sync.ManagedGroup)
		if err != nil {
			return failed(err)
		}
		r := reconcile.RunOnce(pctx, cfg, src, tgt, mailer, time.Now(), *dryRun, log)
		log.Info("sync finished", "ok", r.OK(), "errors", len(r.Errors))
		return r.OK()
	}

	log.Info("google2ipa starting", "version", version, "dry_run", *dryRun, "interval", interval.String())
	if *interval <= 0 {
		if once() {
			return 0
		}
		return 1
	}
	for {
		once()
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return 0
		case <-time.After(*interval):
		}
	}
}

func newLogger(c config.Log) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(c.Level)) // validated in config
	opts := &slog.HandlerOptions{Level: lvl}
	if c.Format == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
