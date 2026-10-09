package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"trie-trunk/go/codec"
)

type event struct {
	at     time.Time
	fields codec.Record
}

type milestone struct {
	Minute  int    `json:"minute"`
	Service uint16 `json:"service"`
	Kind    uint16 `json:"kind"`
	Release string `json:"release"`
}

var services = map[uint16]string{1: "gateway", 2: "billing", 3: "vault", 4: "worker"}

var milestones = []milestone{
	{26 * 60, 2, 2, "2.8.0"}, {26*60 + 2, 2, 3, "2.8.0"},
	{27*60 + 30, 2, 5, "2.7.4"}, {27*60 + 38, 2, 4, "2.7.4"},
	{68 * 60, 4, 3, "1.12.0"}, {68*60 + 25, 4, 4, "1.12.0"},
	{113 * 60, 3, 2, "3.4.0"}, {113*60 + 3, 3, 3, "3.4.0"},
	{114*60 + 15, 3, 4, "3.4.0"},
	{153 * 60, 1, 2, "4.2.1"}, {153*60 + 2, 1, 3, "4.2.1"},
	{153*60 + 5, 1, 4, "4.2.1"},
	{166 * 60, 2, 3, "2.7.4"}, {166*60 + 8, 2, 4, "2.7.4"},
}

func definition() codec.Definition {
	return codec.Definition{
		Object: "logs", Version: 1 << 20, Timezone: "UTC",
		Storage:      &codec.StoragePolicy{Format: 4, Boundary: "resource", Bucket: "hour"},
		Path:         []codec.Key{11, 3, 4, 5, 2, 6},
		Aggregations: []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"},
		Fields: codec.Schema{
			0: {Name: "id", Encoding: "uuidv7"},
			1: {Name: "duration", Unit: "us"},
			2: {Name: "level", Enums: map[uint16]string{1: "INFO", 2: "WARN", 3: "ERROR"}},
			3: {Name: "service", Enums: services},
			4: {Name: "resource", Enums: map[uint16]string{1: "routes", 2: "invoices", 3: "secrets", 4: "jobs", 5: "runtime"}},
			5: {Name: "operation", Enums: map[uint16]string{1: "route", 2: "charge", 3: "decrypt", 4: "dispatch", 5: "deploy", 6: "restart", 7: "ready", 8: "rollback"}},
			6: {Name: "event_type", Enums: map[uint16]string{1: "request", 2: "deployment", 3: "server_restart", 4: "service_ready", 5: "rollback"}},
			7: {Name: "message", Enums: map[uint16]string{1: "Request completed", 2: "Dependency timeout", 3: "Upstream unavailable", 4: "Request latency elevated", 5: "Deployment started", 6: "Server restarting", 7: "Service healthy", 8: "Release rolled back"}},
			8: {Name: "http_status"}, 9: {Name: "instance"}, 10: {Name: "release"},
			11: {Name: "environment", Enums: map[uint16]string{1: "demo"}},
			12: {Name: "synthetic"},
		},
	}
}

func week(end time.Time) []event {
	start := end.Add(-7 * 24 * time.Hour)
	rng := rand.New(rand.NewSource(5410))
	events := make([]event, 0, 4500)
	for minute := 0; minute < 7*24*60; minute += 5 {
		at := start.Add(time.Duration(minute) * time.Minute)
		count := 2
		if at.Hour() < 8 {
			count = 1
		}
		if at.Hour() >= 12 && at.Hour() < 18 {
			count = 3
		}
		for i := 0; i < count; i++ {
			service := uint16(rng.Intn(4) + 1)
			chance := 2
			affected := uint16(0)
			switch {
			case minute >= 26*60 && minute < 27*60+30:
				affected, chance = 2, 78
			case minute >= 67*60+40 && minute < 68*60+25:
				affected, chance = 4, 65
			case minute >= 113*60 && minute < 114*60+15:
				affected, chance = 3, 57
			case minute >= 165*60+55 && minute < 166*60+8:
				affected, chance = 2, 85
			}
			if affected != 0 && rng.Intn(100) < 70 {
				service = affected
			}
			if service != affected {
				chance = 2
			}
			severity, message, status := uint16(1), uint16(1), uint16(200)
			duration := 1200 + rng.Intn(90000)
			if rng.Intn(100) < chance {
				severity, message, status = 3, uint16(2+rng.Intn(2)), 503
				duration = 400000 + rng.Intn(2600000)
			} else if rng.Intn(100) < 5 {
				severity, message = 2, 4
				duration = 180000 + rng.Intn(500000)
			}
			timestamp := at.Add(time.Duration(i*300/count+rng.Intn(300/count)) * time.Second)
			events = append(events, event{timestamp, codec.Record{
				1: uint64(duration), 2: severity, 3: service, 4: service, 5: service,
				6: uint16(1), 7: message, 8: status, 9: fmt.Sprintf("%s-%02d", services[service], rng.Intn(3)+1),
				10: release(minute, service), 11: uint16(1), 12: true,
			}})
		}
	}
	for _, m := range milestones {
		events = append(events, event{start.Add(time.Duration(m.Minute) * time.Minute), codec.Record{
			1: uint64(0), 2: uint16(1), 3: m.Service, 4: uint16(5), 5: m.Kind + 3,
			6: m.Kind, 7: m.Kind + 3, 9: fmt.Sprintf("%s-01", services[m.Service]),
			10: m.Release, 11: uint16(1), 12: true,
		}})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	for i := range events {
		e := &events[i]
		hash := sha256.Sum256([]byte(fmt.Sprintf("snoot-demo-v1:%d:%d", e.at.UnixMilli(), i)))
		var id codec.UUIDv7
		copy(id[:], hash[:16])
		var millis [8]byte
		binary.BigEndian.PutUint64(millis[:], uint64(e.at.UnixMilli()))
		copy(id[:6], millis[2:])
		id[6] = id[6]&15 | 0x70
		id[8] = id[8]&63 | 0x80
		e.fields[0] = id
	}
	return events
}

func release(minute int, service uint16) string {
	version := []string{"", "4.2.0", "2.7.4", "3.3.2", "1.12.0"}[service]
	for _, m := range milestones {
		if m.Service == service && minute >= m.Minute {
			version = m.Release
		}
	}
	return version
}

func run() error {
	definitionPath := flag.String("definition", "", "write only the demo object definition as JSON")
	output := flag.String("out", "", "new output directory for framed CBOR and manifest")
	endText := flag.String("end", time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339), "exclusive end of the seven-day window (RFC3339)")
	flag.Parse()
	if *definitionPath != "" {
		if *output != "" || flag.NArg() != 0 {
			return fmt.Errorf("--definition cannot combine with --out or positional arguments")
		}
		data, err := json.MarshalIndent(definition(), "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(*definitionPath, append(data, '\n'), 0644)
	}
	if *output == "" || flag.NArg() != 0 {
		return fmt.Errorf("use -out <new-directory> [-end <RFC3339>]")
	}
	end, err := time.Parse(time.RFC3339, *endText)
	if err != nil {
		return err
	}
	end = end.UTC()
	if end.Add(-7*24*time.Hour).Before(time.Unix(0, 0)) || uint64(end.UnixMilli()) >= 1<<48 {
		return fmt.Errorf("week outside UUIDv7 timestamp range")
	}
	if err := os.Mkdir(*output, 0755); err != nil {
		return err
	}
	d := definition()
	revision, schema, err := d.Prepare()
	if err != nil {
		return err
	}
	framed, err := codec.AppendFrame(nil, schema)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "schema.cbor"), framed, 0644); err != nil {
		return err
	}
	events := week(end)
	f, err := os.Create(filepath.Join(*output, "events.cbor"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	errors := 0
	for _, event := range events {
		if event.fields[2] == uint16(3) {
			errors++
		}
		payload, err := codec.MarshalCompact(event.fields, codec.CBOR)
		if err != nil {
			return err
		}
		framed, err = codec.AppendFrame(framed[:0], payload)
		if err != nil {
			return err
		}
		if _, err := w.Write(framed); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	manifest := map[string]any{"from": end.Add(-7 * 24 * time.Hour), "to": end, "events": len(events), "errors": errors, "version": codec.VersionString(revision.Version), "digest": hex.EncodeToString(revision.Digest[:]), "milestones": milestones}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "manifest.json"), append(data, '\n'), 0644); err != nil {
		return err
	}
	fmt.Printf("%d events, %d errors, %d lifecycle events: %s to %s\n", len(events), errors, len(milestones), manifest["from"], end)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
