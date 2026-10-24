package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPACCompressionPolicy(t *testing.T) {
	if (Policy{}).PAC.level() != 1 {
		t.Fatal("default must remain fast")
	}
	for _, level := range []int{-1, 0, 1, 6, 9, 10} {
		raw := fmt.Sprintf("version=1\ntimezone='UTC'\n[defaults.pac]\ngzip_level=%d\n", level)
		c, err := ParseConfiguration([]byte(raw))
		if level < 1 || level > 9 {
			if err == nil {
				t.Fatalf("accepted level %d", level)
			}
		} else if err != nil || c.Resolve("payment/transactions.v1").PAC.level() != level {
			t.Fatalf("level %d: %v", level, err)
		}
	}
	c, err := ParseConfiguration([]byte("version=1\ntimezone='UTC'\n[defaults.pac]\ngzip_level=9\n[resources.'payment/transactions.v1'.pac]\ngzip_level=3\n"))
	if err != nil || c.Resolve("payment/transactions.v1").PAC.level() != 3 || c.Resolve("other/resource.v1").PAC.level() != 9 {
		t.Fatal("registry inheritance", err)
	}
	path := filepath.Join(t.TempDir(), "debug.cfg")
	for _, inherit := range []bool{true, false} {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("version=1\ninherit=%t\n", inherit)), 0600); err != nil {
			t.Fatal(err)
		}
		p, _, err := readScope(path, c.Defaults, true)
		want := 1
		if inherit {
			want = 9
		}
		if err != nil || p.PAC.level() != want {
			t.Fatal("scope inheritance", inherit, p, err)
		}
	}
}

func TestPACCompressionReloadAndStoredBytes(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	now := at.Add(75 * time.Minute)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l,
		Now: func() time.Time { return now }}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	writePolicy := func(name string, level int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(s.collection, name+".cfg"),
			[]byte(fmt.Sprintf("version=1\n[policy.pac]\ngzip_level=%d\n", level)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writePolicy("resource", 6)
	writePolicy("error", 9)
	if err := s.ReloadPolicy(); err != nil {
		t.Fatal(err)
	}
	if s.policyFor(Read).PAC.level() != 6 || s.policyFor(Error).PAC.level() != 9 {
		t.Fatal("branch override did not inherit")
	}
	var original []byte
	for i, level := range []int{9, 3} {
		now = at.Add(75*time.Minute + time.Duration(i)*time.Hour)
		writePolicy("error", level)
		if err := s.ReloadPolicy(); err != nil {
			t.Fatal(err)
		}
		instant := at.Add(time.Duration(i) * time.Hour)
		for n := uint32(1); n <= 20; n++ {
			if _, err := s.Append(Error, testFrame(t, l, instant.Add(time.Duration(n)*time.Second), n, false)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		b := s.branches[Error]
		run := b.runs[i]
		raw, err := readBytes(b.pop, run.PopStart, run.PopEnd)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := expandPAC(raw)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := compressPAC(decoded, level)
		if err != nil || !bytes.Equal(raw, expected) {
			t.Fatal("sealing ignored configured level", level, err)
		}

		if i == 0 {
			original = append([]byte(nil), raw...)
		}
	}
	writePolicy("error", 10)
	if err := s.ReloadPolicy(); err == nil || s.policyFor(Error).PAC.level() != 3 {
		t.Fatal("invalid reload changed active compression policy")
	}
	writePolicy("error", 3)
	if _, err := s.Append(Write, testFrame(t, l, at, 100, false)); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if s.branches[Write].runs[0].PAC.Gzip {
		t.Fatal("compression policy must not compress write storage")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := s.branches[Error]
	raw, err := readBytes(b.pop, b.runs[0].PopStart, b.runs[0].PopEnd)
	if err != nil || !bytes.Equal(raw, original) {
		t.Fatal("old PAC changed after reload/recovery", err)
	}
	if events := collect(t, s, Error, at, at.Add(2*time.Hour)); len(events) != 40 {
		t.Fatal("mixed-level recovery lost events", len(events))
	}
}
