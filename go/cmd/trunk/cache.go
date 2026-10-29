package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
	"trie-trunk/go/server/cache"
)

func runCache(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: trunk cache init|ingest|search|verify|prune|prefer|definitions|cleanup --cache name --tenant owner [--definition file] [--dir ./caches] [--branch write] [--from RFC3339 --to RFC3339]")
	}
	command := args[0]
	if command != "init" && command != "ingest" && command != "search" && command != "verify" && command != "prune" && command != "prefer" && command != "definitions" && command != "cleanup" {
		return fmt.Errorf("unknown cache command")
	}
	flags := flag.NewFlagSet("cache "+command, flag.ContinueOnError)
	root := flags.String("dir", "./caches", "cache storage root")
	name := flags.String("cache", "", "registered cache key")
	tenant := flags.String("tenant", "", "registered owner")
	definition := flags.String("definition", "", "unified resource.rx or JSON definition")
	branch := flags.String("branch", "write", "read, write, error, debug or trace")
	from := flags.String("from", "", "inclusive RFC3339 time")
	to := flags.String("to", "", "exclusive RFC3339 time")
	apply := flags.Bool("apply", false, "apply verified offline cleanup; default is preview")
	objects := flags.Bool("object-index", false, "enable optional write history index")
	if e := flags.Parse(args[1:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected cache arguments")
	}
	if *apply && command != "cleanup" {
		return fmt.Errorf("--apply is only valid for cleanup")
	}
	if command == "cleanup" {
		report, err := cache.Cleanup(ctx, cache.Config{Root: *root, Cache: *name, Tenant: *tenant}, *apply)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(report)
	}
	if command == "definitions" {
		endpoint, e := cache.OpenEndpoint(cache.Config{Root: *root, Cache: *name, Tenant: *tenant})
		if e != nil {
			return e
		}
		defer endpoint.Close()
		definitions, e := endpoint.Definitions()
		if e != nil {
			return e
		}
		return json.NewEncoder(output).Encode(definitions)
	}
	raw, e := os.ReadFile(*definition)
	if e != nil {
		return e
	}
	var layout *cache.Layout
	if len(raw) > 0 && raw[0] == '{' {
		var d cache.Definition
		if e = json.Unmarshal(raw, &d); e == nil {
			layout, e = cache.Compile(d)
		}
	} else {
		layout, e = cache.ParseDefinition(raw)
	}
	if e != nil {
		return e
	}
	if command == "prefer" {
		// Deliberately do not pass Layout to startup: compare against the
		// committed preference before installing or activating the new map.
		endpoint, e := cache.OpenEndpoint(cache.Config{Root: *root, Cache: *name, Tenant: *tenant, ObjectIndex: *objects})
		if e != nil {
			return e
		}
		defer endpoint.Close()
		if e = endpoint.Prefer(layout); e != nil {
			return e
		}
		_, e = fmt.Fprintf(output, "Preferred %s.v%d minor %d: %x\n", layout.Definition.Identity, layout.Definition.Resource.Major, layout.Definition.Resource.Minor, layout.Digest)
		return e
	}
	b, e := cache.ParseBranch(*branch)
	if e != nil {
		return e
	}
	store, e := cache.Open(cache.Config{Root: *root, Cache: *name, Tenant: *tenant, Layout: layout, ObjectIndex: *objects})
	if e != nil {
		return e
	}
	defer store.Close()
	switch command {
	case "init":
		return store.Close()
	case "prune":
		return store.ApplyRetention()
	case "verify":
		if e = store.Flush(); e != nil {
			return e
		}
		_, e = fmt.Fprintf(output, "Verified cache %s, definition %s version %d; row width %d bytes.\n", *name, layout.Definition.Identity, layout.Definition.Version, layout.RowLayout().Width)
		return e
	case "ingest":
		reader := bufio.NewReaderSize(input, 128<<10)
		var frame []byte
		for {
			if e = ctx.Err(); e != nil {
				return e
			}
			frame, e = layout.ReadFrame(reader, frame)
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			if _, e = store.Append(b, frame); e != nil {
				return e
			}
		}
		return store.Close()
	case "search":
		start, e := time.Parse(time.RFC3339Nano, *from)
		if e != nil {
			return e
		}
		end, e := time.Parse(time.RFC3339Nano, *to)
		if e != nil {
			return e
		}
		writer := bufio.NewWriterSize(output, 128<<10)
		e = store.Search(ctx, cache.Query{Branch: b, From: start, To: end}, func(_ uint64, frame []byte) error { _, e := writer.Write(frame); return e })
		if e != nil {
			return e
		}
		return writer.Flush()
	}
	return nil
}
