package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPromotionValidatesRegisteredProducerTranslations(t *testing.T) {
	producer, d := referenceLayouts(t)
	current, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: current})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	path := filepath.Join(p.api.Store.collection, fmt.Sprintf("producer-%x.map", producer.Digest))
	if err = ensureMap(path, producer.Bytes); err != nil {
		t.Fatal(err)
	}
	d.Version++
	d.Resource.Minor++
	d.Resource.References = cloneReferences(d.Resource.References)
	for i := range d.Resource.References {
		d.Resource.References[i].Source = fmt.Sprintf("%x", current.Digest)
	}
	next, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Prefer(next); err == nil {
		t.Fatal("promotion stranded registered producer")
	}
	if p.api.Store.layout.Digest != current.Digest {
		t.Fatal("failed compatibility check changed preference")
	}
	d.Resource.References = append(d.Resource.References, current.Definition.Resource.References...)
	next, err = Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Prefer(next); err != nil {
		t.Fatal(err)
	}
}

func nextMinor(t *testing.T, l *Layout) *Layout {
	t.Helper()
	d := l.Definition
	d.Version++
	d.Resource.Minor++
	next, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestPreferredDefinitionSurvivesRestartAndUnpromotedMinor(t *testing.T) {
	base := testLayout(t, false)
	next := nextMinor(t, base)
	future := nextMinor(t, next)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: base}
	p, err := OpenEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { p.Close() }()
	if err = p.Prefer(next); err != nil {
		t.Fatal(err)
	}
	// Installing an immutable map is not approval to send newly encoded data.
	p.mu.Lock()
	err = p.install(future)
	p.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if p.api.Store.layout.Digest != next.Digest {
		t.Fatal("installation promoted future minor")
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	// A stale --definition startup argument must not silently downgrade the
	// persisted preference, or a prepared future map implicitly promote it.
	p, err = OpenEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.api.Store.layout.Digest != next.Digest {
		t.Fatal("restart lost preference")
	}
	status, err := p.Definitions()
	if err != nil || len(status) != 3 {
		t.Fatal(status, err)
	}
	selected := 0
	for _, s := range status {
		if s.Preferred {
			selected++
			if s.Minor != 1 {
				t.Fatal(s)
			}
		}
	}
	if selected != 1 {
		t.Fatal(status)
	}
	if err = p.Prefer(base); err == nil {
		t.Fatal("downgrade accepted")
	}
	if err = p.Prefer(future); err != nil {
		t.Fatal(err)
	}
	p.Close()
	cfg.Layout = nil
	p, err = OpenEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.api.Store.layout.Digest != future.Digest {
		t.Fatal("promotion not durable")
	}
}

func TestFailedPreferencePublishKeepsPreviousSelection(t *testing.T) {
	base := testLayout(t, false)
	next := nextMinor(t, base)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: base}
	p, err := OpenEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.mu.Lock()
	err = p.install(next)
	p.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.Root, cfg.Cache, "registry.map")
	prior, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	// A nonempty directory prevents atomic rename without platform-specific
	// permissions behavior. Restore the catalog before releasing the writer.
	if err = os.WriteFile(filepath.Join(path, "blocked"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	defer func() { os.RemoveAll(path); os.Rename(path+".saved", path) }()
	if err = p.Prefer(next); err == nil {
		t.Fatal("failed publication was reported successful")
	}
	if p.api.Store.layout.Digest != base.Digest {
		t.Fatal("failed preference became active")
	}
	for _, api := range p.apis {
		if api.Store.catalog.Preferred[collectionKey(base)] != base.Digest {
			t.Fatal("shared in-memory catalog changed")
		}
	}
	unchanged, err := os.ReadFile(path + ".saved")
	if err != nil || !bytes.Equal(prior, unchanged) {
		t.Fatal("previous catalog changed", err)
	}
}

func TestLegacyCatalogPreferenceUpgradeAndCorruption(t *testing.T) {
	base := testLayout(t, false)
	next := nextMinor(t, base)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: base}
	for _, l := range []*Layout{base, next} {
		cfg.Layout = l
		s, err := Open(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Layout = nil
	p, err := OpenEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.api.Store.layout.Digest != next.Digest {
		t.Fatal("legacy catalog lost newest-minor default")
	}
	p.Close()
	path := filepath.Join(cfg.Root, cfg.Cache, "registry.map")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c Catalog
	if err = decoder.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Preferred[collectionKey(base)] != next.Digest {
		t.Fatal("legacy upgrade not durable")
	}
	c.Preferred[collectionKey(base)] = [32]byte{123}
	raw, err = encoder.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = replace(path, raw); err != nil {
		t.Fatal(err)
	}
	if p, err = OpenEndpoint(cfg); err == nil {
		p.Close()
		t.Fatal("unknown preferred map accepted")
	}
}
