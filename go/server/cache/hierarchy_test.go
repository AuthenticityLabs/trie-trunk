package cache

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestOptionalHierarchyOverridesAndReload(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("trunk.cfg", "version=1\n[policy]\nobject_index=true\n[policy.staging]\nmax_bytes=1000\nmax_records=200\n[policy.retention]\nread='30d'\n[policy.replication]\nindex_period='4h'\n")
	l := testLayout(t, false)
	n := l.Definition.Resource
	resource := filepath.Join("caches", "demo", n.Service, n.Resource+".v1")
	write("caches/demo/cache.cfg", "version=1\n[policy]\nobject_index=false\n[policy.retention]\nread='14d'\n")
	write(filepath.Join("caches", "demo", n.Service, "service.cfg"), "version=1\n[policy.staging]\nmax_bytes=0\n[policy.retention]\nread='7d'\n")
	write(filepath.Join(resource, "resource.cfg"), "version=1\n[policy.retention]\nread='3d'\n")
	write(filepath.Join(resource, "debug.cfg"), "version=1\ninherit=false\n[policy.retention]\ndebug='1h'\n[policy.replication]\nindex_period='6h'\n")
	s, err := Open(Config{Root: filepath.Join(root, "caches"), Cache: "demo", Tenant: "test", Layout: l})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.config.ObjectIndex || s.policy.Staging.MaxBytes != 0 || s.policy.Staging.MaxRecords != 200 || s.policy.Retention.Read != "3d" || s.policy.Replication.hours() != 4 {
		t.Fatalf("wrong resolved policy: %+v", s.policy)
	}
	if p := s.policyFor(Debug); p.Retention.Read != "" || p.Retention.Debug != "1h" || p.Replication.hours() != 6 {
		t.Fatalf("branch reset: %+v", p)
	}
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	windows, err := s.PlanBranchPackages(Debug, at, at.Add(time.Hour))
	if err != nil || len(windows) != 1 || windows[0].To.Sub(windows[0].From) != 6*time.Hour {
		t.Fatal(windows, err)
	}
	old, oldBranches := s.policy, s.branchPolicies
	write(filepath.Join(resource, "debug.cfg"), "version=1\n[policy]\nobject_index=true\n")
	if err = s.ReloadPolicy(); err == nil || !reflect.DeepEqual(old, s.policy) || !reflect.DeepEqual(oldBranches, s.branchPolicies) {
		t.Fatal("invalid reload changed active policy", err)
	}
	write(filepath.Join(resource, "debug.cfg"), "version=1\n[policy.replication]\nindex_period='1h'\n")
	if err = s.ReloadPolicy(); err != nil {
		t.Fatal(err)
	}
	if s.policyFor(Debug).Retention.Read != "3d" || s.policyFor(Debug).Replication.hours() != 1 {
		t.Fatal("reload failed")
	}
}
