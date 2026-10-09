package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	if e := run(os.Args[1:]); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}

func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	var err error
	switch args[0] {
	case "tap":
		err = runTap(args[1:])
	case "migrate-events":
		err = runMigrate(ctx, args[1:])
	case "search":
		err = runSearch(ctx, args[1:], os.Stdout)
	case "ingest", "serve":
		err = runServices(ctx, args)
	case "--help", "-h":
		fmt.Print(usage + "\n")
	default:
		err = fmt.Errorf("%s", usage)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

type serviceCommand struct {
	name string
	args []string
}

func splitServices(args []string) ([]serviceCommand, error) {
	var commands []serviceCommand
	seen := map[string]bool{}
	for len(args) > 0 {
		name := args[0]
		if name != "ingest" && name != "serve" || seen[name] {
			return nil, fmt.Errorf("expected ingest and/or serve, each at most once")
		}
		seen[name] = true
		args = args[1:]
		i := 0
		for i < len(args) && args[i] != "ingest" && args[i] != "serve" {
			arg := args[i]
			i++
			if strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") && arg != "--help" && arg != "-h" && i < len(args) {
				i++
			}
		}
		commands = append(commands, serviceCommand{name, args[:i]})
		args = args[i:]
	}
	return commands, nil
}

func runServices(ctx context.Context, args []string) error {
	commands, err := splitServices(args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, len(commands))
	for _, command := range commands {
		go func() {
			if command.name == "ingest" {
				done <- runIngestContext(ctx, command.args)
			} else {
				done <- runServe(ctx, command.args)
			}
		}()
	}
	var result error
	for range commands {
		result = errors.Join(result, <-done)
		cancel()
	}
	return result
}

const usage = `usage:
  trunk tap create <tap> [--dir ./taps] [--definition definition.json]
    Register a named object definition; versions are object major / definition minor.
  trunk migrate-events --from ./logs --to ./taps [--timezone America/New_York]
    Offline, verified copy into a new root. Does not switch the running service.
  trunk ingest 5410 serve 443
  trunk serve [443] [--dir ./taps] [--socket path] [--queries 4]
    Serve HTTP/2 (TCP) and HTTP/3 (UDP), plus a private local Unix socket.
    Requires --cert, --key and TRUNK_TOKEN or --client-ca.
    --http-address 127.0.0.1:3020 adds an HTTP origin for a trusted local proxy.
    Each command's flags follow its own name and port.
  trunk search [--endpoint https://host[:port] | --socket path | --local]
    Default: <dir>/.trunk/search.sock. --local reads files without a server.
    Remote TLS: --ca file [--client-cert file --client-key file].
  trunk ingest [5140] [--dir ./taps]
    Local WebSocket: ws://127.0.0.1:5140/<tap>, no credentials needed.
  trunk ingest [5140] --dir ./taps --cert cert.pem --key key.pem [--transport websocket|http3]
    Set TRUNK_TOKEN or use --client-ca ca.pem.
    Loads .env, then .env.<ENVIRONMENT> when selected; --env path selects one file.
    Precedence: explicit arguments > process environment > profile > .env > defaults.
  trunk search [--tail [N] | --head [N]] [--pretty] [-f]
  trunk search start [end] [--dir ./taps] [--tap name] [--pretty] [-f]
  trunk search --from start [--tail [N] | --to end] [-f]
  trunk search --head [N] --to end [-f]
  trunk search -w start[,end] [--dir ./taps] [--tap name] [--pretty] [-f]
  trunk search 14:00,15:00 --where 'service=="vault" http_status==401' --head 25
    Default: today's UTC window through now, latest 100 matches as JSONL.
    --pretty indents JSON objects. --all explicitly searches all stored history.
    --head selects earliest matches; --tail selects latest; omitted N means 100.
    Both work within a window; head and tail cannot combine.
    -w, --win and --window are equivalent; exclude --from and --to.
    Bare timestamps also specify a window; separate bounds with a space or comma.
    Flags may appear before or after timestamps. Other bare arguments are invalid.
    An explicit start and end have no default count limit; --head/--tail override.
    --where supports ==, !=, is null, is !null on top-level fields.
    Whitespace joins predicates within a group; repeated --where groups are alternatives.
    Use JSON scalar values. Explicit AND, OR and parentheses are not accepted.
    Times: 14:30[:15], 2026-09-25, 20260925T14:30, RFC3339, now-15m, today, yesterday.
    Default timezone UTC; explicit offsets are honored. Start inclusive, end exclusive.
    Omitted window end means now, or remains open with --follow.
    -f or --follow streams new matches after the initial selection; Ctrl-C stops.
    --entry-headers name,name or TRUNK_ENTRY_HEADERS orders top-level JSON fields.
    Set ENVIRONMENT=sand in .env to automatically load .env.sand.`
