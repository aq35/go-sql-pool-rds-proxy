// lab is the load client of this repository. One binary, one scenario per run:
//
//	./lab --scenario=<name> [scenario flags]      (use run.sh on the EC2 instance: it sets the DB_* environment)
//	./lab --list                                  scenarios and what they answer
//	./lab --scenario=<name> -h                    flags of one scenario (unknown flags are an error)
//
// To add a scenario: create scenario_<name>.go with a func(args []string) error and register it in `scenarios`.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

type scenario struct {
	summary string
	run     func(args []string) error
}

var scenarios = map[string]scenario{
	"pool-load": {"Go pools (containers x workers) under steady load: MaxIdleConns / lifetime / pinning (GET_LOCK, prepared) / pool rotation / secret rotation", runPoolLoad},
	"rotation":  {"Secrets Manager password rotation behind the Proxy: do existing connections survive, do new logins work, how fast are old connections replaced (pinned or not)", runRotationCmd},
	"trigger":   {"What makes the Proxy open or keep backend connections? One action on K idle sockets (connect, ping, select1, prepared, getlock, ...)", runTriggerCmd},
	"capacity":  {"How much concurrency can the Aurora instance use? QPS / p99 / client CPU per concurrency level (the knee)", runCapacityCmd},
	"patterns":  {"Probes of Go pool / GORM behaviour in front of the Proxy: warmup, serial vs parallel, timeout cascade, GORM pool needs, pinning patterns, lifetime stampede (--mode=...)", runPatternsCmd},
	"exceed":    {"When can Proxy DatabaseConnections exceed ClientConnections? short-lived connections, bursts, per-session settings", runExceedCmd},
}

// commonFlags are defined by every scenario so that a run is always bounded and reports on the same cadence.
type commonFlags struct {
	duration    time.Duration
	reportEvery time.Duration
}

func (c *commonFlags) register(fs *flag.FlagSet, defDuration time.Duration) {
	fs.DurationVar(&c.duration, "duration", defDuration, "total run time (the run stops by itself)")
	fs.DurationVar(&c.reportEvery, "report-every", 15*time.Second, "client-side stats interval")
}

// runContext returns a context that ends after duration or on SIGINT/SIGTERM.
func runContext(duration time.Duration) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, duration)
	return ctx, func() { cancel(); stop() }
}

func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ExitOnError) // unknown / malformed flags exit with status 2
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if len(os.Args) > 1 && os.Args[1] == "--list" {
		names := make([]string, 0, len(scenarios))
		for n := range scenarios {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("%-10s %s\n", n, scenarios[n].summary)
		}
		return
	}
	if len(os.Args) < 2 || !strings.HasPrefix(os.Args[1], "--scenario=") {
		fmt.Fprintln(os.Stderr, "usage: lab --scenario=<name> [flags]   (lab --list shows the scenarios)")
		os.Exit(2)
	}
	name := strings.TrimPrefix(os.Args[1], "--scenario=")
	sc, ok := scenarios[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown scenario %q (lab --list)\n", name)
		os.Exit(2)
	}
	if err := sc.run(os.Args[2:]); err != nil {
		if code, ok := err.(exitCode); ok {
			log.Print(err)
			os.Exit(int(code))
		}
		log.Fatal(err)
	}
}

// exitCode lets a scenario ask for a specific status (3 = the requested load was not reached, see lab/exp.py).
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }
