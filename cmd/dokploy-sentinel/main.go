// Command dokploy-sentinel is a host-level watchdog for Dokploy / Docker VMs that
// warns before memory over-subscription (and disk/swap/crash-loop trouble) takes
// the box down. It runs as a short-lived process on a systemd timer.
//
// Subcommands:
//
//	run       collect → evaluate → maybe notify → write report (the timer target)
//	  --dry-run   evaluate + print, but never send or mutate state
//	check     validate the config file (used by the installer before arming)
//	selftest  on-host readiness probes (docker reachable, targets 2xx, dirs writable)
//	version   print the build version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/alert"
	"github.com/akaike-byob/dokploy-sentinel/internal/app"
	"github.com/akaike-byob/dokploy-sentinel/internal/clock"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const defaultConfigPath = "/etc/dokploy-sentinel/config.toml"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	case "check":
		os.Exit(cmdCheck(os.Args[2:]))
	case "selftest":
		os.Exit(cmdSelftest(os.Args[2:]))
	case "version", "--version", "-v":
		fmt.Printf("dokploy-sentinel %s\n", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dokploy-sentinel — host watchdog for Dokploy / Docker

usage:
  dokploy-sentinel run       [--config PATH] [--dry-run]
  dokploy-sentinel check     [--config PATH]
  dokploy-sentinel selftest  [--config PATH]
  dokploy-sentinel version
`)
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath, "path to config.toml")
	dryRun := fs.Bool("dry-run", false, "evaluate and print, but do not send or mutate state")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		// Runtime config failure: never silently exit into permanence — report it
		// loudly on stderr and fail non-zero so the heartbeat/OnFailure fires.
		fmt.Fprintf(os.Stderr, "config error:\n%v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	runner := &app.Runner{
		Cfg:       cfg,
		Clock:     clock.Real{},
		Sender:    alert.NewSender(&http.Client{Timeout: 10 * time.Second}, alert.SlackRenderer{}, nil),
		Heartbeat: alert.NewHeartbeat(cfg.HeartbeatURL, nil),
		DryRun:    *dryRun,
	}

	res, err := runner.RunOnce(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		return 1
	}

	if *dryRun {
		printDryRun(res)
	} else {
		fmt.Printf("run #%d: %d finding(s), %d suppressed, %d notification(s)\n",
			res.Report.RunID, len(res.Report.Findings), len(res.Report.Suppressed), len(res.Sends))
	}
	return 0
}

func printDryRun(res *app.RunResult) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res.Report)
	fmt.Fprintf(os.Stderr, "\n[dry-run] %d decision(s) — not sent:\n", len(res.Decisions))
	for _, d := range res.Decisions {
		fmt.Fprintf(os.Stderr, "  %-9s %-5s %s → %v\n", d.Alert.Kind, d.Alert.Tier, d.Alert.Key, d.Targets)
	}
}

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath, "path to config.toml")
	_ = fs.Parse(args)

	if _, err := config.Load(*cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "config INVALID:\n%v\n", err)
		return 1
	}
	fmt.Printf("config OK: %s\n", *cfgPath)
	return 0
}

func cmdSelftest(args []string) int {
	fs := flag.NewFlagSet("selftest", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath, "path to config.toml")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config INVALID:\n%v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results := app.Selftest(ctx, cfg)
	failed := 0
	for _, r := range results {
		mark := "ok  "
		if !r.OK {
			mark = "FAIL"
			failed++
		}
		fmt.Printf("[%s] %-28s %s\n", mark, r.Name, r.Detail)
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "\nselftest: %d check(s) failed\n", failed)
		return 1
	}
	fmt.Println("\nselftest: all checks passed")
	return 0
}
