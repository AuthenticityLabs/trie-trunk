// seed-demo creates disposable, synthetic collection-native data for Snoot.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"trie-trunk/go/server/cache"
)

type scenario struct{ service, resource, operation string }

var scenarios = []scenario{{"gateway", "routes", "route"}, {"billing", "invoices", "charge"}, {"vault", "secrets", "decrypt"}, {"worker", "jobs", "dispatch"}, {"payment", "transactions", "authorize"}}

type result struct {
	Resource string
	Events   int
	Branches map[string]int
}
type report struct {
	Synthetic     bool
	Cache, Tenant string
	From, To      time.Time
	Events        int
	Resources     []result
}

func objectID(n uint64) cache.Value {
	var id [16]byte
	id[6] = 0x40
	binary.BigEndian.PutUint64(id[8:], n)
	id[8] |= 0x80
	return cache.Value{State: cache.Direct, Bytes: id[:]}
}

func definition(s scenario) cache.Definition {
	return cache.Definition{PIL: 1, Identity: s.service + "/" + s.resource, Version: 1 << 20, AdaptiveIntegers: "16", Fields: []cache.Field{
		{Name: "event_id", Type: "UUID"}, {Name: "id", Type: "UUID"}, {Name: "revision_id", Type: "U32"}, {Name: "duration", Type: "U64"},
		{Name: "level", Type: "STRING", Values: []string{"INFO", "WARN", "ERROR", "DEBUG", "TRACE"}},
		{Name: "operation", Type: "STRING", Values: []string{s.operation, "read", "deploy", "restart", "ready"}},
		{Name: "message", Type: "STRING"}, {Name: "http_status", Type: "U16"}, {Name: "instance", Type: "STRING"}, {Name: "release", Type: "STRING"},
		{Name: "environment", Type: "STRING", Values: []string{"demo"}}, {Name: "synthetic", Type: "BOOL"},
		{Name: "state", Type: "STRING", Values: []string{"pending", "authorized", "canceled"}}, {Name: "amount", Type: "U32"},
		{Name: "access", Type: "STRING", Values: []string{"read", "write"}},
	}, Resource: cache.Binding{Format: cache.Format, Service: s.service, Resource: s.resource, Major: 1, Timezone: "America/New_York", Roles: cache.Roles{Event: "event_id", Object: "id", Revision: "revision_id", Duration: "duration"}, Path: []string{"access", "level", "operation", "state"}, Aggregations: []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}}}
}
func seed(root string, count, days int, end time.Time) (report, error) {
	return seedWithClock(root, count, days, end, time.Now)
}
func seedWithClock(root string, count, days int, end time.Time, clock func() time.Time) (r report, err error) {
	return seedWithMemoryBudget(root, count, days, end, clock, 0)
}

// Bulk footprint measurements can supply a native budget without changing the
// application's default. Zero retains the normal Store configuration.
func seedWithMemoryBudget(root string, count, days int, end time.Time, clock func() time.Time, nativeMemoryBytes uint64) (r report, err error) {
	if count < len(scenarios) || days < 1 || days > 3650 {
		return r, fmt.Errorf("events must be >=5 and days must be 1..3650")
	}
	if _, e := os.Lstat(root); !os.IsNotExist(e) {
		return r, fmt.Errorf("destination must not exist: %s", root)
	}
	if e := os.MkdirAll(filepath.Dir(root), 0700); e != nil {
		return r, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(root), ".seed-demo-")
	if e != nil {
		return r, e
	}
	defer os.RemoveAll(stage)
	start := end.Add(-time.Duration(days) * 24 * time.Hour)
	r = report{Synthetic: true, Cache: "demo", Tenant: "local", From: start, To: end, Events: count}
	// Persist replay enablement for every collection; a per-open flag alone is
	// lost when the coordinator later reopens the cache with its default config.
	policy := "version = 1\ntimezone = \"America/New_York\"\n\n[defaults.replication]\nindex_period = \"1h\"\npopulation = \"lazy\"\nprovisional_tail = \"15m\"\n"
	for _, spec := range scenarios {
		policy += fmt.Sprintf("\n[resources.%q]\nobject_index = true\n", spec.service+"/"+spec.resource+".v1")
	}
	if e = os.MkdirAll(filepath.Join(stage, r.Cache), 0700); e != nil {
		return r, e
	}
	if e = os.WriteFile(filepath.Join(stage, r.Cache, "registry.cfg"), []byte(policy), 0600); e != nil {
		return r, e
	}
	for n, spec := range scenarios {
		l, e := cache.Compile(definition(spec))
		if e != nil {
			return r, e
		}
		cfg := cache.Config{Root: stage, Cache: r.Cache, Tenant: r.Tenant, Layout: l, ObjectIndex: true, Now: clock, NativeMemoryBytes: nativeMemoryBytes}
		store, e := cache.Open(cfg)
		if e != nil {
			return r, e
		}
		result := result{Resource: l.Definition.Identity, Branches: map[string]int{}}
		batches := map[cache.Branch][][]byte{}
		revisions := map[uint64]uint64{}
		flush := func() error {
			for b := cache.Read; b <= cache.Trace; b++ {
				if len(batches[b]) == 0 {
					continue
				}
				if _, e := store.AppendBatch(b, batches[b]); e != nil {
					return e
				}
				batches[b] = nil
			}
			return nil
		}
		for i := n; i < count; i += len(scenarios) {
			at := time.UnixMilli(start.UnixMilli() + (end.UnixMilli()-start.UnixMilli())*int64(i)/int64(count))
			var id [16]byte
			binary.BigEndian.PutUint64(id[:8], uint64(at.UnixMilli())<<16)
			id[6] = 0x70
			id[8] = 0x80
			binary.BigEndian.PutUint32(id[12:], uint32(i+1))
			j := i / len(scenarios)
			obj := uint64(j%250 + 1)
			branch := cache.Read
			level, operation, status := uint64(0), uint64(1), uint64(200)
			message := "Request completed"
			duration := uint64(1200 + (i*7919)%90000)
			phase := int(at.Sub(start).Hours())
			burst := phase%(7*24) >= 26+n*2 && phase%(7*24) < 28+n*2
			if j%3 != 0 {
				branch = cache.Write
				operation = 0
			}
			// Preserve access even when the event is stored in a diagnostic partition.
			access := uint64(0)
			if branch == cache.Write {
				access = 1
			}
			if j%53 == 0 || burst && j%4 != 0 {
				branch = cache.Error
				level = 2
				status = 503
				message = "Dependency timeout"
				duration = uint64(400000 + (i*3571)%2600000)
			} else if j%31 == 0 {
				level = 1
				message = "Request latency elevated"
				duration = uint64(180000 + (i*7919)%500000)
			} else if j%47 == 0 {
				branch = cache.Debug
				level = 3
				message = "Cache lookup inspected"
			} else if j%71 == 0 {
				branch = cache.Trace
				level = 4
				message = "Dependency request span"
			}
			if j%997 < 3 {
				access = 0
				branch = cache.Read
				level = 0
				operation = uint64(2 + j%997)
				status = 200
				duration = 0
				message = []string{"Deployment started", "Server restarting", "Service ready"}[j%997]
			}

			v := []cache.Value{{State: cache.Direct, Bytes: id[:]}, objectID(obj), {}, cache.Uint(duration, 8), cache.Uint(level, 1), cache.Uint(operation, 1), cache.Text(message), cache.Uint(status, 2), cache.Text(fmt.Sprintf("%s-%02d", spec.service, j%3+1)), cache.Text(fmt.Sprintf("1.%d.%d", n, phase/168)), cache.Uint(0, 1), {State: cache.Direct, Bytes: []byte{1}}, {}, {}, cache.Uint(access, 1)}
			if branch == cache.Write {
				revisions[obj]++
				v[2] = cache.Uint(revisions[obj], 4)
				v[12] = cache.Uint((revisions[obj]-1)%3, 1)
				if revisions[obj] == 1 || j%4 == 0 {
					v[13] = cache.Uint(uint64(100+(i*37)%200000), 4)
				}
			}
			raw, e := l.Encode(nil, v)
			if e != nil {
				store.Close()
				return r, e
			}
			batches[branch] = append(batches[branch], raw)
			result.Events++
			result.Branches[branch.Name()]++
			if result.Events%512 == 0 {
				if e = flush(); e != nil {
					store.Close()
					return r, e
				}
			}
		}
		if e = flush(); e != nil {
			store.Close()
			return r, e
		}
		if e = store.Close(); e != nil {
			return r, e
		}
		// Read back after reopening, including the durable provisional tail.
		store, e = cache.Open(cfg)
		if e != nil {
			return r, e
		}
		for b := cache.Read; b <= cache.Trace; b++ {
			actual := 0
			e = store.SearchLive(context.Background(), cache.Query{Branch: b, From: start, To: end}, func(raw []byte) error {
				var f cache.Frame
				if e := l.Scan(raw, &f); e != nil {
					return e
				}
				if len(f.Values[11].Bytes) != 1 || f.Values[11].Bytes[0] != 1 {
					return fmt.Errorf("missing synthetic marker")
				}
				actual++
				return nil
			})
			if e != nil || actual != result.Branches[b.Name()] {
				store.Close()
				return r, fmt.Errorf("verify %s/%s: %d != %d: %v", spec.service, b.Name(), actual, result.Branches[b.Name()], e)
			}
		}
		if e = store.Close(); e != nil {
			return r, e
		}
		r.Resources = append(r.Resources, result)
	}
	endpoint, e := cache.OpenEndpoint(cache.Config{Root: stage, Cache: r.Cache, Tenant: r.Tenant, Now: clock, NativeMemoryBytes: nativeMemoryBytes})
	if e != nil {
		return r, e
	}
	for _, spec := range scenarios {
		e = endpoint.History(context.Background(), spec.service+"/"+spec.resource, 1, objectID(42), start, end, func(cache.EventReference, []byte) error { return nil })
		if e != nil {
			endpoint.Close()
			return r, e
		}
	}
	if e = endpoint.Close(); e != nil {
		return r, e
	}
	data, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return r, e
	}
	if e = os.WriteFile(filepath.Join(stage, "demo-data.json"), data, 0600); e != nil {
		return r, e
	}
	if _, e = os.Lstat(root); !os.IsNotExist(e) {
		return r, fmt.Errorf("destination appeared while seeding")
	}
	if e = os.Rename(stage, root); e != nil {
		return r, e
	}
	return r, nil
}
func main() {
	root := flag.String("dir", "./caches", "new cache root (must not exist)")
	count := flag.Int("events", 50000, "total synthetic events")
	days := flag.Int("days", 30, "history length")
	flag.Parse()
	// Keep the generated history safely beyond the live three-cycle staging tail.
	r, e := seed(*root, *count, *days, time.Now().UTC().Add(-20*time.Minute).Truncate(time.Second))
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(r)
}
