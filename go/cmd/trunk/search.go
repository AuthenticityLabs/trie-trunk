package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"trie-trunk/go/cmd/trunk/os"
	"trie-trunk/go/server/search"
)

type searchOptions struct {
	search.Config
	Connection search.Connection
	Local      bool
}

func runSearch(ctx context.Context, args []string, output io.Writer) error {
	config, err := parseSearch(args)
	if err != nil {
		return err
	}
	if config.Follow {
		restore := os.QuietTerminal()
		defer restore()
	}
	if config.Local {
		return search.Run(ctx, config.Config, output)
	}
	return search.Pull(ctx, config.Config, config.Connection, output)
}

func parseSearch(args []string) (searchOptions, error) {
	return parseSearchAt(args, time.Now())
}

func parseSearchAt(args []string, now time.Time) (searchOptions, error) {
	var config searchOptions
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	flags.String("env", ".env", "environment file; empty disables file loading")
	flags.BoolVar(&config.Local, "local", false, "search files directly without a server")
	flags.StringVar(&config.Connection.Transport, "transport", "auto", "remote transport: auto (HTTP/3 then HTTP/2), http3 or http2")
	flags.StringVar(&config.Connection.Endpoint, "endpoint", "", "HTTPS search server; default local socket")
	flags.StringVar(&config.Connection.Socket, "socket", "", "Unix socket; default <dir>/.trunk/search.sock")
	ca := flags.String("ca", "", "server CA certificate")
	cert := flags.String("client-cert", "", "mTLS client certificate")
	key := flags.String("client-key", "", "mTLS client private key")
	root := flags.String("dir", "./taps", "store root")
	tap := flags.String("tap", "", "search one tap; default searches all taps")
	pretty := flags.Bool("pretty", false, "indent JSON objects; default is JSONL")
	jsonl := flags.Bool("jsonl", false, "explicitly select JSONL output (the default)")
	headers := flags.String("entry-headers", "", "comma-separated top-level JSON fields to render first")
	window := flags.String("win", "", "start[,end]: date, time, now-15m, today or yesterday; default timezone UTC")
	flags.StringVar(window, "w", "", "alias for --win")
	flags.StringVar(window, "window", "", "alias for --win")
	from := flags.String("from", "", "inclusive timestamp or relative start")
	to := flags.String("to", "", "exclusive timestamp or relative end")
	head := flags.Uint64("head", 100, "earliest N matches within the window; omitted N means 100")
	tail := flags.Uint64("tail", 100, "latest N matches within the window; omitted N means 100")
	all := flags.Bool("all", false, "search all stored history; default window is today in UTC")
	flags.Func("where", "whitespace-separated predicates must all match; repeat --where for alternatives", func(value string) error {
		config.Where = append(config.Where, value)
		return nil
	})
	flags.BoolVar(&config.Follow, "follow", false, "keep streaming newly appended matches until interrupted")
	flags.BoolVar(&config.Follow, "f", false, "alias for --follow")
	positional, err := parseSearchArgs(flags, args)
	if err != nil {
		return config, err
	}
	env, err := applyEnvironment(flags, "dir", "entry-headers", "endpoint", "socket", "ca", "client-cert", "client-key")
	if err != nil {
		return config, err
	}
	config.Connection.Token, _ = env.lookup("TRUNK_TOKEN")
	if config.Connection.Transport != "auto" && config.Connection.Transport != "http2" && config.Connection.Transport != "http3" {
		return config, fmt.Errorf("transport must be auto, http2 or http3")
	}
	if config.Connection.Transport != "auto" && config.Connection.Endpoint == "" {
		return config, fmt.Errorf("remote transport requires --endpoint; local search uses the Unix socket")
	}
	if config.Local && (config.Connection.Endpoint != "" || config.Connection.Socket != "") {
		return config, fmt.Errorf("cannot combine --local with an endpoint or socket")
	}
	if config.Connection.Endpoint != "" && config.Connection.Socket != "" {
		return config, fmt.Errorf("cannot combine --endpoint with --socket")
	}
	if config.Connection.Endpoint != "" {
		config.Connection.TLS, err = clientTLS(*ca, *cert, *key)
		if err != nil {
			return config, err
		}
	}
	seen := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	hasWindow := seen["w"] || seen["win"] || seen["window"]
	if len(positional) != 0 {
		if hasWindow || seen["from"] || seen["to"] {
			return config, fmt.Errorf("cannot combine positional timestamps with --win, --window, --from or --to")
		}
		*window, hasWindow = strings.Join(positional, " "), true
	}
	if hasWindow && (seen["from"] || seen["to"]) {
		return config, fmt.Errorf("cannot combine a search window with --from or --to")
	}
	if *all && (hasWindow || seen["from"] || seen["to"]) {
		return config, fmt.Errorf("cannot combine --all with timestamps")
	}
	if seen["head"] && seen["tail"] {
		return config, fmt.Errorf("cannot combine --head with --tail")
	}
	config.Root, config.Tap = *root, *tap
	config.Headers, config.Pretty = strings.Split(*headers, ","), *pretty && !*jsonl
	config.Start, config.End = now.UTC().Truncate(24*time.Hour), now.UTC()
	if config.Follow {
		config.End = time.UnixMilli(1 << 48).UTC()
	}
	if *all || seen["to"] && !seen["from"] {
		config.Start = time.UnixMilli(0).UTC()
	}
	explicitEnd := seen["to"]
	if hasWindow {
		var err error
		config.Start, config.End, err = search.ParseWindow(*window, now)
		if err != nil {
			return config, err
		}
		explicitEnd = !config.End.IsZero()
		if !explicitEnd {
			config.End = now.UTC()
			if config.Follow {
				config.End = time.UnixMilli(1 << 48).UTC()
			}
		}
	}
	if seen["from"] {
		var err error
		config.Start, err = search.ParseTime(*from, now)
		if err != nil {
			return config, fmt.Errorf("invalid start time: %w", err)
		}
	}
	if seen["to"] {
		var err error
		config.End, err = search.ParseTime(*to, now)
		if err != nil {
			return config, fmt.Errorf("invalid end time: %w", err)
		}
	}
	if config.Start.After(config.End) || config.Start.Equal(config.End) && (hasWindow || seen["from"] || seen["to"]) {
		return config, fmt.Errorf("search end time must be after start time")
	}
	switch {
	case seen["head"]:
		config.Limit = head
	case seen["tail"]:
		config.Limit, config.Tail = tail, true
	case (hasWindow || seen["from"]) && explicitEnd:
	default:
		config.Limit, config.Tail = tail, !seen["to"]
	}
	return config, nil
}

func parseSearchArgs(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) != 0 {
		arg := args[0]
		if arg == "--" {
			for _, value := range args[1:] {
				if strings.TrimSpace(value) == "" {
					return nil, fmt.Errorf("empty positional timestamp")
				}
			}
			return append(positional, args[1:]...), nil
		}
		if len(arg) < 2 || arg[0] != '-' || arg[1] >= '0' && arg[1] <= '9' {
			if strings.TrimSpace(arg) == "" {
				return nil, fmt.Errorf("empty positional timestamp")
			}
			positional = append(positional, arg)
			args = args[1:]
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		count := 1
		if !hasValue && (name == "head" || name == "tail") {
			value := "100"
			if len(args) > 1 {
				if _, err := strconv.ParseUint(args[1], 10, 64); err == nil {
					value, count = args[1], 2
				}
			}
			if err := flags.Set(name, value); err != nil {
				return nil, err
			}
		} else {
			if option := flags.Lookup(name); option != nil && !hasValue && len(args) > 1 {
				boolean, ok := option.Value.(interface{ IsBoolFlag() bool })
				if !ok || !boolean.IsBoolFlag() {
					count = 2
				}
			}
			if err := flags.Parse(args[:count]); err != nil {
				return nil, err
			}
		}
		args = args[count:]
	}
	return positional, nil
}
