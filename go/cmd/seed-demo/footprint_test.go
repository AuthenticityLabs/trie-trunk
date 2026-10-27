package main

// Opt-in reproducible measurement, not a timing assertion or production exporter.
import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"trie-trunk/go/server/cache"
)

type footprintFile struct {
	Name      string `json:"name"`
	Bytes     int64  `json:"bytes"`
	Allocated int64  `json:"allocated_bytes"`
	Component string `json:"component"`
}

func TestDemoFootprint(t *testing.T) {
	out := os.Getenv("TRUNK_FOOTPRINT_OUT")
	if out == "" {
		t.Skip("set TRUNK_FOOTPRINT_OUT for opt-in measurement")
	}
	count := 50000
	if s := os.Getenv("TRUNK_FOOTPRINT_EVENTS"); s != "" {
		var e error
		count, e = strconv.Atoi(s)
		if e != nil {
			t.Fatal(e)
		}
	}
	if e := os.MkdirAll(out, 0755); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(t.TempDir(), "caches")
	end := time.Date(2026, 10, 20, 18, 41, 34, 0, time.UTC)
	clock := func() time.Time { return end.Add(2 * time.Hour) }
	var nativeMemoryBytes uint64
	if value := os.Getenv("TRUNK_FOOTPRINT_NATIVE_BYTES"); value != "" {
		var err error
		nativeMemoryBytes, err = strconv.ParseUint(value, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	objectIndex := false
	if value := os.Getenv("TRUNK_FOOTPRINT_COLLECTIONS"); value != "" {
		var err error
		objectIndex, err = strconv.ParseBool(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	manifest, e := seedWithStorageProfile(root, count, 30, end, clock, nativeMemoryBytes, objectIndex)
	if e != nil {
		t.Fatal(e)
	}
	seedElapsed := time.Since(started)
	t.Logf("seeded and verified %d events in %s", count, seedElapsed)
	expanded, e := os.Create(filepath.Join(out, "expanded.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	defer expanded.Close()
	payload, e := os.Create(filepath.Join(out, "payload.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	defer payload.Close()
	exHash, payHash := sha256.New(), sha256.New()
	exWriter, payWriter := bufio.NewWriter(io.MultiWriter(expanded, exHash)), bufio.NewWriter(io.MultiWriter(payload, payHash))
	exEncoder, payEncoder := json.NewEncoder(exWriter), json.NewEncoder(payWriter)
	exEncoder.SetEscapeHTML(false)
	payEncoder.SetEscapeHTML(false)
	ids := map[string]bool{}
	events := 0
	for _, spec := range scenarios {
		t.Logf("exporting and indexing %s/%s", spec.service, spec.resource)
		l, err := cache.Compile(definition(spec))
		if err != nil {
			t.Fatal(err)
		}
		s, err := cache.Open(cache.Config{Root: root, Cache: manifest.Cache, Tenant: manifest.Tenant, Layout: l, ObjectIndex: objectIndex, Now: clock, NativeMemoryBytes: nativeMemoryBytes})
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if e := s.Close(); e != nil {
					t.Error(e)
				}
			}()
			for b := cache.Read; b <= cache.Trace; b++ {
				err = s.SearchLive(context.Background(), cache.Query{Branch: b, From: manifest.From, To: manifest.To}, func(raw []byte) error {
					var frame cache.Frame
					if e := l.Scan(raw, &frame); e != nil {
						return e
					}
					object := map[string]any{}
					for n, f := range l.Definition.Fields {
						v := frame.Values[n]
						if v.State == cache.Absent {
							continue
						}
						if v.State == cache.Null {
							object[f.Name] = nil
							continue
						}
						u := func() uint64 {
							var x uint64
							for j := len(v.Bytes) - 1; j >= 0; j-- {
								x = x<<8 | uint64(v.Bytes[j])
							}
							return x
						}
						if len(f.Values) > 0 && v.State == cache.Direct {
							code := u()
							if code >= uint64(len(f.Values)) {
								return fmt.Errorf("invalid dictionary value")
							}
							object[f.Name] = f.Values[code]
							continue
						}
						switch f.Type {
						case "UUID":
							if len(v.Bytes) != 16 {
								return fmt.Errorf("invalid UUID")
							}
							x := v.Bytes
							object[f.Name] = fmt.Sprintf("%x-%x-%x-%x-%x", x[:4], x[4:6], x[6:8], x[8:10], x[10:])
						case "U8", "U16", "U32", "U64":
							object[f.Name] = u()
						case "BOOL":
							if len(v.Bytes) != 1 {
								return fmt.Errorf("invalid bool")
							}
							object[f.Name] = v.Bytes[0] != 0
						case "STRING":
							object[f.Name] = string(v.Bytes)
						default:
							return fmt.Errorf("footprint fixture exporter needs type %s", f.Type)
						}
					}
					eventID, ok := object["event_id"].(string)
					if !ok {
						return fmt.Errorf("missing event UUID")
					}
					if ids[eventID] {
						return fmt.Errorf("duplicate event UUID")
					}
					ids[eventID] = true
					if object["synthetic"] != true {
						return fmt.Errorf("fixture marker missing")
					}
					if e := payEncoder.Encode(object); e != nil {
						return e
					}
					object["cache"], object["service"], object["resource"], object["branch"] = manifest.Cache, spec.service, spec.resource+".v1", b.Name()
					var ts [8]byte
					copy(ts[2:], frame.Values[0].Bytes[:6])
					object["time"] = time.UnixMilli(int64(binary.BigEndian.Uint64(ts[:]))).UTC().Format("2006-01-02T15:04:05.000Z")
					if e := exEncoder.Encode(object); e != nil {
						return e
					}
					events++
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				// A full-text query forces synchronous catch-up, so the footprint cannot
				// accidentally credit a partially built Tantivy index as a storage saving.
				q := url.Values{"tap": {fmt.Sprintf("%s/%s/%s.v1/%s", manifest.Cache, spec.service, spec.resource, b.Name())}, "from": {manifest.From.Format(time.RFC3339Nano)}, "to": {manifest.To.Format(time.RFC3339Nano)}, "page": {"1"}, "include": {"summary"}, "query": {`{"search":{"text":"__absent_footprint_probe__","mode":"phrase"},"order":"asc"}`}}
				req := httptest.NewRequest("GET", "/events?"+q.Encode(), nil)
				req.Header.Set("Accept", "application/pil")
				req.Header.Set("PIL-Version", "2")
				req.Header.Set("PIL-Features", "pac-arrays,adaptive-integers,view-buckets")
				w := httptest.NewRecorder()
				(&cache.HTTP{Store: s}).ServeHTTP(w, req)
				if w.Code != 200 || w.Header().Get("Trunk-Format") != "pac-view/0" {
					t.Fatalf("search catch-up %d: %s", w.Code, w.Body.String())
				}
			}
		}()
	}
	if events != manifest.Events {
		t.Fatal(events, manifest.Events)
	}
	if e = exWriter.Flush(); e != nil {
		t.Fatal(e)
	}
	if e = payWriter.Flush(); e != nil {
		t.Fatal(e)
	}
	if e = expanded.Close(); e != nil {
		t.Fatal(e)
	}
	if e = payload.Close(); e != nil {
		t.Fatal(e)
	}
	exInfo, e := os.Stat(expanded.Name())
	if e != nil {
		t.Fatal(e)
	}
	payInfo, e := os.Stat(payload.Name())
	if e != nil {
		t.Fatal(e)
	}
	var files []footprintFile
	groups := map[string]int64{}
	var total, allocated int64
	e = filepath.WalkDir(filepath.Join(root, manifest.Cache), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.Type().IsRegular() {
			return nil
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		name, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if filepath.Ext(path) == ".sum" || filepath.Ext(path) == ".pods" {
			// Preserve measured packets for validation and paired codec controls,
			// outside the measured cache directory.
			folder := "summaries"
			if filepath.Ext(path) == ".pods" {
				folder = "pods"
			}
			target := filepath.Join(out, folder, name)
			if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
				return e
			}
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if e = os.WriteFile(target, raw, 0644); e != nil {
				return e
			}
		}
		component := filepath.Ext(path)
		if strings.Contains(filepath.ToSlash(name), ".search/") {
			component = "tantivy"
		} else if strings.HasPrefix(filepath.Base(path), "collection.") {
			component = filepath.Base(path)
		} else if component == "" {
			component = "other"
		}
		blocks := st.Sys().(*syscall.Stat_t).Blocks * 512
		files = append(files, footprintFile{name, st.Size(), blocks, component})
		groups[component] += st.Size()
		total += st.Size()
		allocated += blocks
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	result := map[string]any{"events": events, "from": manifest.From, "to": manifest.To, "fixture": "seed-demo; five resources; 15 fields; adaptive integers=16; all five branches", "expanded_jsonl_bytes": exInfo.Size(), "payload_only_jsonl_bytes": payInfo.Size(), "expanded_jsonl_sha256": hex.EncodeToString(exHash.Sum(nil)), "payload_jsonl_sha256": hex.EncodeToString(payHash.Sum(nil)), "trie_trunk_bytes": total, "trie_trunk_allocated_bytes": allocated, "by_component": groups, "files": files, "file_count": len(files), "seed_and_verification_ns": seedElapsed.Nanoseconds()}

	result["native_memory_budget_override"] = nativeMemoryBytes
	result["object_index"] = objectIndex
	result["pac_gzip_level"] = 1
	result["notes"] = []string{
		"Every event read back; expanded names, dictionaries, UUIDs, booleans and numerics; counts and unique UUIDs verified.",
		"Expanded JSONL includes time and routing context; payload JSONL excludes that derived context.",
		"All regular datastore files counted after close and synchronous Tantivy catch-up. Logical and allocated bytes are separate.",
		fmt.Sprintf("Collection indexing enabled: %t. All branches use default gzip-1 unless collection replay requires uncompressed writes. Incompressible packs remain plain.", objectIndex),
		"Seeding duration includes generation, ingress, sealing, indexing and read-back; not a pure ingestion benchmark.",
		"JSONL is ordered by resource, branch, then time. Exact Pods and summaries retained for paired measurements.",
	}
	raw, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(out, "footprint.json"), append(raw, '\n'), 0644); e != nil {
		t.Fatal(e)
	}
	t.Logf("%d events: expanded=%d payload=%d trie=%d allocated=%d", events, exInfo.Size(), payInfo.Size(), total, allocated)
}
