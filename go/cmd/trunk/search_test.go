package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/store"
)

func TestSearchRejectsInvalidArguments(t *testing.T) {
	clearTrunkEnvironment(t)
	t.Chdir(t.TempDir())
	const start = "20260923T00:00"
	const end = "20260924T00:00"
	const window = start + "," + end
	for _, tc := range []struct {
		name    string
		args    []string
		message string
	}{
		{"window and from", []string{"--window", window, "--from", start}, "cannot combine"},
		{"to and window", []string{"--to", end, "--window", window}, "cannot combine"},
		{"all three", []string{"--from", start, "--to", end, "--window", window}, "cannot combine"},
		{"empty from conflicts", []string{"--window", window, "--from="}, "cannot combine"},
		{"empty to conflicts", []string{"--to=", "--window", window}, "cannot combine"},
		{"empty window conflicts", []string{"--window=", "--from", start, "--to", end}, "cannot combine"},
		{"empty window", []string{"--window="}, "window requires"},
		{"extra timestamp", []string{"--window", window + "," + end}, "window requires"},
		{"empty range end", []string{"--window", start + ","}, "window requires"},
		{"empty range start", []string{"--window", "," + end}, "window requires"},
		{"equal times", []string{"--window", start + "," + start}, "after start"},
		{"reversed times", []string{"--from", end, "--to", start}, "after start"},
		{"invalid date", []string{"--window", "20260230T00:00,20260301T00:00"}, "invalid start time"},
		{"missing flag value", []string{"--window"}, "flag needs an argument"},
		{"unknown option", []string{"--unknown"}, "flag provided but not defined"},
		{"invalid boolean", []string{"--window", window, "--jsonl=invalid"}, "invalid"},
		{"extra operand", []string{"--window", window, "extra"}, "cannot combine"},
		{"old quoted form", []string{"window(20260923 00:00, 20260924 00:00)"}, "invalid start time"},
		{"old positional form", []string{"window", "20260923", "00:00,", "20260924", "00:00"}, "invalid start time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runLocalSearch(context.Background(), tc.args, &out)
			if err == nil || !strings.Contains(err.Error(), tc.message) || out.Len() != 0 {
				t.Fatal(out.String(), err)
			}
		})
	}
}

func TestSearchCommandWindowAndFromTo(t *testing.T) {
	clearTrunkEnvironment(t)
	t.Chdir(t.TempDir())
	root := t.TempDir()
	at := time.Date(2026, 9, 23, 12, 0, 0, 999000000, time.UTC)
	d := codec.Definition{Version: 42, Timezone: "UTC", Fields: codec.Schema{8: {Name: "id", Encoding: "uuidv7"}, 12: {Name: "duration", Unit: "us"}, 15: {Name: "message"}}, Aggregations: []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}}
	rev, data, err := d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	m, err := store.Open(store.Config{Root: root, Tap: "sandbox", Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.Install(rev, data); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		id, err := codec.NewUUIDv7(at.Add(-time.Duration(i) * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		b, err := codec.MarshalCompact(codec.Record{8: id, 12: uint64(2234 + i), 15: "first line\nsecond line <ok>"}, codec.PIL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = m.Append(rev, b); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	const start = "20260923T11:59"
	const end = "20260923T12:01"
	const window = start + "," + end
	var lines, pretty bytes.Buffer
	if err = runLocalSearch(context.Background(), []string{"--window", window, "--dir", root}, &lines); err != nil {
		t.Fatal(err)
	}
	var selected bytes.Buffer
	if err = runLocalSearch(context.Background(), []string{"--window", window, "--dir", root, "--tap", "sandbox", "--jsonl"}, &selected); err != nil || !bytes.Equal(selected.Bytes(), lines.Bytes()) {
		t.Fatal("tap selection changed search results", err)
	}
	parts := bytes.Split(bytes.TrimSuffix(lines.Bytes(), []byte{'\n'}), []byte{'\n'})
	if len(parts) != 2 {
		t.Fatalf("not JSONL: %q", lines.String())
	}
	for _, line := range parts {
		if !json.Valid(line) {
			t.Fatal(string(line))
		}
	}
	if strings.Contains(lines.String(), `\u003c`) {
		t.Fatal("escaped readable text")
	}
	if err = runLocalSearch(context.Background(), []string{"--dir", root, "--from", start, "--to", end, "--pretty"}, &pretty); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pretty.String(), "\n  \"id\":") {
		t.Fatal(pretty.String())
	}
	for _, args := range [][]string{
		{start, end, "--dir", root},
		{window, "--dir=" + root},
		{start + ",", "--dir", root, end},
		{"--dir", root, "-w", "2026-09-23T07:59-04:00,2026-09-23T08:01-04:00"},
		{"--dir", root, "--win", "2026-09-23T11:59:59.999Z,2026-09-23T12:00:00.999000001Z"},
		{"--dir=" + root, "--jsonl", "--window=" + window},
		{"--to", end, "--dir", root, "--from", start, "--jsonl"},
		{"--from=" + start, "--jsonl", "--to=" + end, "--dir", root},
	} {
		var equivalent bytes.Buffer
		if err = runLocalSearch(context.Background(), args, &equivalent); err != nil || !bytes.Equal(equivalent.Bytes(), lines.Bytes()) {
			t.Fatal(args, equivalent.String(), err)
		}
	}

	if err := os.WriteFile(".env.sand", []byte("TRUNK_DIR='"+root+"'\nTRUNK_ENTRY_HEADERS=time,duration,level,service,resource,operation,id\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var ordered bytes.Buffer
	if err := runLocalSearch(context.Background(), []string{"--env", ".env.sand", "--window", window, "--jsonl"}, &ordered); err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSuffix(ordered.Bytes(), []byte{'\n'}), []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte(`{"time":`)) || !json.Valid(line) {
			t.Fatal("profile order not applied", string(line))
		}
	}
	t.Setenv("TRUNK_ENTRY_HEADERS", "id,time")
	ordered.Reset()
	if err := runLocalSearch(context.Background(), []string{"--env", ".env.sand", "--window", window, "--pretty"}, &ordered); err != nil || !strings.HasPrefix(ordered.String(), "{\n  \"id\":") {
		t.Fatal("process environment must override profile", err)
	}
	ordered.Reset()
	if err := runLocalSearch(context.Background(), []string{"--env", ".env.sand", "--window", window, "--jsonl", "--entry-headers", "message"}, &ordered); err != nil || !strings.HasPrefix(ordered.String(), `{"message":`) {
		t.Fatal("flag must override environment", err)
	}
	ordered.Reset()
	if err := runLocalSearch(context.Background(), []string{"--env", ".env.sand", "--window", window, "--jsonl", "--entry-headers="}, &ordered); err != nil || !bytes.Equal(ordered.Bytes(), lines.Bytes()) {
		t.Fatal("empty flag must restore default rendering", err)
	}
	t.Setenv("TRUNK_ENTRY_HEADERS", "")
	if err := os.WriteFile(".env", []byte("ENVIRONMENT=sand\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("TRUNK_ENTRY_HEADERS"); err != nil {
		t.Fatal(err)
	}
	ordered.Reset()
	if err := runLocalSearch(context.Background(), []string{"--window", window, "--pretty"}, &ordered); err != nil || !strings.HasPrefix(ordered.String(), "{\n  \"time\":") {
		t.Fatal("automatic profile selection did not load header order", err)
	}

	t.Setenv("ENVIRONMENT", "")
	t.Setenv("TRUNK_ENTRY_HEADERS", "")
	var windowPretty bytes.Buffer
	if err = runLocalSearch(context.Background(), []string{"--window", window, "--dir", root, "--pretty"}, &windowPretty); err != nil || !bytes.Equal(windowPretty.Bytes(), pretty.Bytes()) {
		t.Fatal(windowPretty.String(), err)
	}
	if err := os.WriteFile(".env", []byte("TRUNK_DIR='"+root+"'\nTRUNK_CERT=missing.pem\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var configured bytes.Buffer
	if err := runLocalSearch(context.Background(), []string{"--window", window, "--jsonl"}, &configured); err != nil || !bytes.Equal(configured.Bytes(), lines.Bytes()) {
		t.Fatal("configured search directory or all-tap behavior", err)
	}

	t.Setenv("TRUNK_DIR", filepath.Join(t.TempDir(), "unused"))
	configured.Reset()
	if err := runLocalSearch(context.Background(), []string{"--window", window, "--jsonl", "--dir", root}, &configured); err != nil || !bytes.Equal(configured.Bytes(), lines.Bytes()) {
		t.Fatal("explicit search directory lost precedence", err)
	}
	a, b := json.NewDecoder(&lines), json.NewDecoder(&pretty)
	wantBegin := []string{"2026-09-23T11:59:59.999Z", "2026-09-23T12:00:00.999Z"}
	wantDuration := []float64{2235, 2234}
	for i := 0; i < 2; i++ {
		var x, y map[string]any
		if err = a.Decode(&x); err != nil {
			t.Fatal(err)
		}
		if err = b.Decode(&y); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(x, y) {
			t.Fatal(x, y)
		}
		if x["time"] != wantBegin[i] || x["duration"] != wantDuration[i] {
			t.Fatal("missing or inaccurate time or duration", x)
		}
		for _, name := range []string{"time_begin", "time_start", "time_end"} {
			if _, exists := x[name]; exists {
				t.Fatal("obsolete derived timestamp", name, x)
			}
		}
	}
	if err = a.Decode(new(any)); err != io.EOF {
		t.Fatal(err)
	}
	if err = runLocalSearch(context.Background(), []string{"--window", window, "--dir", root}, failedOutput{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	var empty bytes.Buffer
	if err = runLocalSearch(context.Background(), []string{"--from", "20260923T13:00", "--to", "20260923T14:00", "--dir", root}, &empty); err != nil || empty.Len() != 0 {
		t.Fatal(empty.String(), err)
	}
}

func TestSearchFlexibleWindow(t *testing.T) {
	clearTrunkEnvironment(t)
	t.Chdir(t.TempDir())
	now := time.Date(2026, 9, 25, 16, 30, 15, 123456789, time.UTC)
	for _, name := range []string{"-w", "--win", "--window"} {
		config, err := parseSearchAt([]string{name, "now-15m"}, now)
		if err != nil || !config.Start.Equal(now.Add(-15*time.Minute)) || !config.End.Equal(now) || config.Limit == nil || *config.Limit != 100 || !config.Tail {
			t.Fatal(name, config, err)
		}
		config, err = parseSearchAt([]string{name, "now-15m", "-f"}, now)
		if err != nil || !config.End.Equal(time.UnixMilli(1<<48)) || !config.Follow {
			t.Fatal(name, config, err)
		}
		config, err = parseSearchAt([]string{name, "now-15m,now", "-f"}, now)
		if err != nil || !config.End.Equal(now) || !config.Follow || config.Limit != nil {
			t.Fatal(name, config, err)
		}
		for _, other := range [][]string{{"--from", "today"}, {"--to", "now"}} {
			args := append([]string{name, "today,now"}, other...)
			if _, err := parseSearchAt(args, now); err == nil || !strings.Contains(err.Error(), "cannot combine") {
				t.Fatal(args, err)
			}
		}
	}
	config, err := parseSearchAt([]string{"--from", "-15m", "--to", "now"}, now)
	if err != nil || config.End.Sub(config.Start) != 15*time.Minute || !config.End.Equal(now) {
		t.Fatal(config, err)
	}
	config, err = parseSearchAt([]string{"-w", "14:00,15:00"}, now)
	if err != nil || config.Start.Day() != 25 || config.Start.Hour() != 14 || config.End.Hour() != 15 {
		t.Fatal(config, err)
	}
	config, err = parseSearchAt([]string{"-w", "now", "-f"}, now)
	if err != nil || !config.Start.Equal(now) || !config.End.Equal(time.UnixMilli(1<<48)) {
		t.Fatal(config, err)
	}
	if _, err := parseSearchAt([]string{"-w", "now+1h"}, now); err == nil {
		t.Fatal("accepted future start after implicit end")
	}
}

func TestSearchPositionalWindows(t *testing.T) {
	clearTrunkEnvironment(t)
	t.Chdir(t.TempDir())
	now := time.Date(2026, 9, 25, 16, 30, 15, 123456789, time.UTC)
	for _, test := range []struct {
		args, explicit []string
	}{
		{[]string{"14:00"}, []string{"-w", "14:00"}},
		{[]string{"14:00", "-f"}, []string{"-w", "14:00", "-f"}},
		{[]string{"14:00", "15:00"}, []string{"-w", "14:00,15:00"}},
		{[]string{"14:00,15:00"}, []string{"-w", "14:00,15:00"}},
		{[]string{"14:00,", "15:00"}, []string{"-w", "14:00,15:00"}},
		{[]string{"14:00", ",", "15:00"}, []string{"-w", "14:00,15:00"}},
		{[]string{"14:00", "--pretty", "15:00", "--tap", "sandbox"}, []string{"--pretty", "--tap", "sandbox", "-w", "14:00,15:00"}},
		{[]string{"--pretty", "14:00", "--tap=sandbox", "15:00", "--pretty=false"}, []string{"--pretty=false", "--tap", "sandbox", "-w", "14:00,15:00"}},
		{[]string{"-15m", "-f"}, []string{"-w", "-15m", "-f"}},
		{[]string{"-f", "-15m"}, []string{"-w", "-15m", "-f"}},
		{[]string{"--pretty", "-15m,now"}, []string{"-w", "-15m,now", "--pretty"}},
		{[]string{"yesterday", "today"}, []string{"-w", "yesterday,today"}},
		{[]string{"2026-09-24", "14:00"}, []string{"-w", "2026-09-24 14:00"}},
		{[]string{"2026-09-24", "14:00,", "2026-09-25", "14:00"}, []string{"-w", "2026-09-24 14:00,2026-09-25 14:00"}},
		{[]string{"--", "14:00", "15:00"}, []string{"-w", "14:00,15:00"}},
	} {
		got, err := parseSearchAt(test.args, now)
		if err != nil {
			t.Fatal(test.args, err)
		}
		want, err := parseSearchAt(test.explicit, now)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal(test.args, got, want, err)
		}
	}
	for _, args := range [][]string{
		{"14:00", "--from", "today"}, {"14:00", "--to", "now"},
		{"14:00", "--win", "today,now"}, {"--window", "today,now", "14:00"},
		{"-w", "14:00", "15:00"},
		{"14:00", "15:00", "16:00"}, {"14:00,15:00", "16:00"},
		{"pretty"}, {"./logs"}, {"100"}, {"14:00", "pretty"}, {"--pretty", "false"},
		{"14:00", "--unknown"}, {"14:00", "--tap"}, {"14:00", "--pretty=invalid"},
		{"--", "14:00", "--pretty"}, {""}, {"", "today"}, {"--", "", "today"},
	} {
		if _, err := parseSearchAt(args, now); err == nil {
			t.Fatal("accepted", args)
		}
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestSearchCountOptions(t *testing.T) {
	clearTrunkEnvironment(t)
	t.Chdir(t.TempDir())
	for _, test := range []struct {
		args                 []string
		count                uint64
		tail, follow, pretty bool
	}{
		{nil, 100, true, false, false},
		{[]string{"--head"}, 100, false, false, false},
		{[]string{"--tail"}, 100, true, false, false},
		{[]string{"--head", "12", "--pretty"}, 12, false, false, true},
		{[]string{"--tail=3", "--follow"}, 3, true, true, false},
		{[]string{"--tail", "0", "-f"}, 0, true, true, false},
		{[]string{"--from", "20260923T00:00", "-f"}, 100, true, true, false},
		{[]string{"--from=20260923T00:00", "--tail", "12"}, 12, true, false, false},
		{[]string{"--head", "12", "--to", "20260924T00:00"}, 12, false, false, false},
		{[]string{"--to", "20260924T00:00"}, 100, false, false, false},
		{[]string{"--head", "--from", "20260923T00:00"}, 100, false, false, false},
		{[]string{"--tail", "--to", "20260924T00:00"}, 100, true, false, false},
		{[]string{"--head", "--window", "20260923T00:00,20260924T00:00"}, 100, false, false, false},
	} {
		config, err := parseSearch(test.args)
		if err != nil {
			t.Fatal(test.args, err)
		}
		if config.Limit == nil || *config.Limit != test.count || config.Tail != test.tail || config.Follow != test.follow || config.Pretty != test.pretty {
			t.Fatal(test.args, config)
		}
	}
	for _, args := range [][]string{
		{"--head", "--tail"},
		{"--all", "--from", "today"}, {"--all", "today,now"}, {"--all", "--to", "today"},
		{"--tail=-1"}, {"--head", "nope"}, {"--from="}, {"--to="},
	} {
		if _, err := parseSearch(args); err == nil {
			t.Fatal("accepted", args)
		}
	}
	config, err := parseSearch([]string{"--from", "20260923T00:00", "--to", "20260924T00:00", "--follow"})
	if err != nil || config.Limit != nil || !config.Follow {
		t.Fatal(config, err)
	}
}

func TestSearchCurrentDayAndClauses(t *testing.T) {
	clearTrunkEnvironment(t)
	t.Chdir(t.TempDir())
	now := time.Date(2026, 9, 24, 23, 30, 0, 0, time.FixedZone("west", -4*3600))
	day := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	for _, args := range [][]string{nil, {"--head"}, {"--tail", "5"}, {"--where", `service=="vault"`}} {
		c, err := parseSearchAt(args, now)
		if err != nil || !c.Start.Equal(day) || !c.End.Equal(now) {
			t.Fatal(args, c, err)
		}
	}
	c, err := parseSearchAt([]string{"-f"}, now)
	if err != nil || !c.Start.Equal(day) || !c.End.Equal(time.UnixMilli(1<<48)) {
		t.Fatal(c, err)
	}
	c, err = parseSearchAt([]string{"--all", "--head", "5"}, now)
	if err != nil || !c.Start.Equal(time.UnixMilli(0)) || !c.End.Equal(now) || c.Tail || *c.Limit != 5 {
		t.Fatal(c, err)
	}
	clauses := []string{`service=="vault" http_status==401`, `service=="billing" error is !null`}
	c, err = parseSearchAt([]string{"14:00,15:00", "--where", clauses[0], "--head", "2", "--where=" + clauses[1]}, now)
	if err != nil || !reflect.DeepEqual(c.Where, clauses) || c.Tail || *c.Limit != 2 || c.Start.Hour() != 14 || c.End.Hour() != 15 {
		t.Fatal(c, err)
	}
	c, err = parseSearchAt(nil, day)
	if err != nil || !c.Start.Equal(day) || !c.End.Equal(day) {
		t.Fatal("midnight must produce an empty default window", c, err)
	}
}

func runLocalSearch(ctx context.Context, args []string, output io.Writer) error {
	return runSearch(ctx, append([]string{"--local"}, args...), output)
}
