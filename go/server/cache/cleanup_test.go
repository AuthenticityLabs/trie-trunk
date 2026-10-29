package cache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cleanupFixture(t *testing.T) (Config, *Layout, string) {
	t.Helper()
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(72 * time.Hour) }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range []Branch{Write, Error} {
		if _, e = s.Append(b, testFrame(t, l, at, 1, false)); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	dir := s.dir
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	return c, l, dir
}
func cloneGeneration(t *testing.T, dir, prefix string, generation [16]byte) []string {
	t.Helper()
	var result []string
	for _, ext := range []string{"idx", "pods", "tri", "journal", "sum"} {
		raw, e := os.ReadFile(filepath.Join(dir, "write."+ext))
		if e != nil {
			t.Fatal(e)
		}
		if ext == "tri" {
			copy(raw[48:64], generation[:])
		} else {
			copy(raw[40:56], generation[:])
		}
		path := filepath.Join(dir, prefix+"."+ext)
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		result = append(result, path)
	}
	return result
}
func TestCleanupPreviewApplyAndReplay(t *testing.T) {
	c, l, dir := cleanupFixture(t)
	var generation [16]byte
	generation[0] = 1
	files := cloneGeneration(t, dir, fmt.Sprintf("write.g%x", generation), generation)
	// Incomplete installation: a valid map published before catalog registration.
	d := l.Definition
	d.Version++
	d.Resource.Minor++
	orphan, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	orphanDir := filepath.Join(filepath.Dir(dir), "1")
	if e = os.Mkdir(orphanDir, 0700); e != nil {
		t.Fatal(e)
	}
	orphanPath := filepath.Join(orphanDir, "resource.rx")
	if e = os.WriteFile(orphanPath, orphan.Bytes, 0600); e != nil {
		t.Fatal(e)
	}
	files = append(files, orphanPath)
	unknown := filepath.Join(dir, "writes.old.pods")
	os.WriteFile(unknown, []byte("keep"), 0600)
	before := map[string][]byte{}
	for _, name := range []string{"resource.rx", "state.tri", "write.idx", "write.pods", "write.tri", "write.journal", "write.sum", "write.seed", "error.seed"} {
		raw, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		before[name] = raw
	}
	report, e := Cleanup(context.Background(), c, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(report.Files) != len(files) || report.Bytes == 0 {
		t.Fatal(report)
	}
	for _, f := range files {
		if _, e = os.Stat(f); e != nil {
			t.Fatal("preview removed file", e)
		}
	}
	report, e = Cleanup(context.Background(), c, true)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range report.Files {
		if !f.Removed {
			t.Fatal(f)
		}
	}
	for _, f := range files {
		if _, e = os.Stat(f); !os.IsNotExist(e) {
			t.Fatal("orphan survived", f, e)
		}
	}
	for name, raw := range before {
		after, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil || !bytes.Equal(after, raw) {
			t.Fatal("changed live file", name, e)
		}
	}
	if raw, e := os.ReadFile(unknown); e != nil || string(raw) != "keep" {
		t.Fatal(e)
	}
	report, e = Cleanup(context.Background(), c, true)
	if e != nil || len(report.Files) != 0 {
		t.Fatal(report, e)
	}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	locations, e := s.ObjectLocations(testObject(42))
	if e != nil || len(locations) != 1 {
		t.Fatal(locations, e)
	}
	if rows := collect(t, s, Write, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), c.Now()); len(rows) != 1 {
		t.Fatal(len(rows))
	}
}
func TestCleanupPreservesReferencedAndDataBearingUnregisteredMaps(t *testing.T) {
	c, l, dir := cleanupFixture(t)
	for minor := uint32(1); minor <= 2; minor++ {
		d := l.Definition
		d.Version += uint64(minor)
		d.Resource.Minor = minor
		orphan, e := Compile(d)
		if e != nil {
			t.Fatal(e)
		}
		target := filepath.Join(filepath.Dir(dir), fmt.Sprint(minor))
		os.Mkdir(target, 0700)
		os.WriteFile(filepath.Join(target, "resource.rx"), orphan.Bytes, 0600)
		if minor == 1 {
			os.WriteFile(filepath.Join(filepath.Dir(dir), fmt.Sprintf("producer-%x.rx", orphan.Digest)), orphan.Bytes, 0600)
		} else {
			os.WriteFile(filepath.Join(target, "write.pods"), []byte("unregistered data"), 0600)
		}
	}
	report, e := Cleanup(context.Background(), c, true)
	if e != nil || len(report.Files) != 0 {
		t.Fatal(report, e)
	}
}
func TestCleanupRefusesWriterCorruptionAndSymlinks(t *testing.T) {
	for _, scenario := range []string{"writer", "orphan-header", "active-data", "missing-active", "symlink", "cancel", "tenant"} {
		t.Run(scenario, func(t *testing.T) {
			c, _, dir := cleanupFixture(t)
			var generation [16]byte
			generation[0] = 1
			files := cloneGeneration(t, dir, fmt.Sprintf("write.g%x", generation), generation)
			ctx := context.Background()
			switch scenario {
			case "writer":
				s, e := Open(c)
				if e != nil {
					t.Fatal(e)
				}
				defer s.Close()
			case "orphan-header":
				os.WriteFile(files[0], []byte("corrupt"), 0600)
			case "active-data":
				raw, _ := os.ReadFile(filepath.Join(dir, "write.pods"))
				raw[len(raw)-1] ^= 1
				os.WriteFile(filepath.Join(dir, "write.pods"), raw, 0600)
			case "missing-active":
				os.Remove(filepath.Join(dir, "write.pods"))
			case "symlink":
				if e := os.Symlink(files[0], filepath.Join(dir, "link")); e != nil {
					t.Fatal(e)
				}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "tenant":
				c.Tenant = "other"
			}
			if _, e := Cleanup(ctx, c, true); e == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			for _, f := range files {
				if _, e := os.Stat(f); e != nil {
					t.Fatal("removed before verification", e)
				}
			}
		})
	}
}
func TestCleanupRetiredDefaultGeneration(t *testing.T) {
	c, _, dir := cleanupFixture(t)
	// Preserve old bytes as if deletion after the committed catalog switch failed.
	old := map[string][]byte{}
	for _, ext := range []string{"idx", "pods", "tri", "journal", "sum"} {
		old[ext], _ = os.ReadFile(filepath.Join(dir, "write."+ext))
	}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	e = s.prune(Write, c.Now())
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	for ext, raw := range old {
		if e = os.WriteFile(filepath.Join(dir, "write."+ext), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	report, e := Cleanup(context.Background(), c, true)
	if e != nil || len(report.Files) != 5 {
		t.Fatal(report, e)
	}
	for _, f := range report.Files {
		if strings.Contains(f.Path, ".g") {
			t.Fatal("selected replacement deleted", f)
		}
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	locations, e := s.ObjectLocations(testObject(42))
	if e != nil || len(locations) != 0 {
		t.Fatal(locations, e)
	}
}

func TestMissingRetainedBranchCannotRecreateRowsOrDeletePriorFiles(t *testing.T) {
	cfg, _, dir := cleanupFixture(t)
	var generation [16]byte
	generation[0] = 1
	prior := cloneGeneration(t, dir, fmt.Sprintf("write.g%x", generation), generation)
	s, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	e = s.prune(Write, cfg.Now())
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	prefix := s.prefix(Write)
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	for _, ext := range []string{".idx", ".pods", ".tri", ".journal", ".sum"} {
		if e = os.Remove(filepath.Join(dir, prefix+ext)); e != nil {
			t.Fatal(e)
		}
	}
	if opened, e := Open(cfg); e == nil {
		opened.Close()
		t.Fatal("missing retained source silently recreated")
	}
	if _, e = Cleanup(context.Background(), cfg, true); e == nil {
		t.Fatal("missing selected source allowed cleanup")
	}
	for _, path := range prior {
		if _, e = os.Stat(path); e != nil {
			t.Fatal("recovery candidate was removed", e)
		}
	}
}
