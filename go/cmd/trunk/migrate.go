package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"trie-trunk/go/server/store"
)

func runMigrate(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("migrate-events", flag.ContinueOnError)
	var o store.MigrationOptions
	f.StringVar(&o.Source, "from", "", "existing storage root (writer must be stopped)")
	f.StringVar(&o.Destination, "to", "", "new, nonexistent storage root")
	f.StringVar(&o.Timezone, "timezone", "", "IANA zone or ±HH:MM; default preserves each schema")
	f.StringVar(&o.Boundary, "boundary", "resource", "trie boundary field")
	f.StringVar(&o.Bucket, "bucket", "hour", "hour or day")
	if e := f.Parse(args); e != nil {
		return e
	}
	if o.Source == "" || o.Destination == "" || f.NArg() != 0 {
		return fmt.Errorf("migrate-events requires --from and --to")
	}
	report, e := store.Migrate(ctx, o)
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
