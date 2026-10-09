package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/store"
)

// Definitions are supplied explicitly: an object identity is not guessed from
// field names, a tap name, or a log-specific default.
func runTap(args []string) error {
	if len(args) < 2 || args[0] != "create" {
		return fmt.Errorf("usage: trunk tap create <tap> [--dir ./taps] [--definition definition.json]")
	}
	tap := args[1]
	if err := store.ValidateTap(tap); err != nil {
		return err
	}
	flags := flag.NewFlagSet("tap create", flag.ContinueOnError)
	flags.String("env", ".env", "environment file; empty disables file loading")
	root := flags.String("dir", "./taps", "store root")
	input := flags.String("definition", "", "named object definition as JSON")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected tap arguments")
	}
	if _, err := applyEnvironment(flags, "dir"); err != nil {
		return err
	}
	var revision codec.Revision
	var data []byte
	if *input != "" {
		raw, err := os.ReadFile(*input)
		if err != nil {
			return err
		}
		var d codec.Definition
		if err = json.Unmarshal(raw, &d); err != nil {
			return err
		}
		if err = codec.ValidateObject(d.Object); err != nil {
			return err
		}
		revision, data, err = d.Prepare()
		if err != nil {
			return err
		}
	}
	m, err := store.Open(store.Config{Root: *root, Tap: tap})
	if err != nil {
		return err
	}
	if len(data) > 0 {
		err = m.Install(revision, data)
	}
	return errors.Join(err, m.Close())
}
