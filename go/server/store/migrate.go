package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"trie-trunk/go/codec"
)

type MigrationOptions struct{ Source, Destination, Timezone, Boundary, Bucket string }
type MigratedSchema struct {
	Tap      string
	Old, New codec.Revision
	Events   uint64
	Checksum [32]byte
}
type MigrationReport struct {
	Source  string
	Schemas []MigratedSchema
}

// Migrate writes and verifies a separate root, then publishes it atomically.
// Source writer locks are held through publication. Source recovery may discard
// incomplete appends; complete source records and files are never renamed/deleted.
func Migrate(ctx context.Context, o MigrationOptions) (report MigrationReport, err error) {
	source, err := filepath.Abs(o.Source)
	if err != nil {
		return report, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return report, err
	}
	destination, err := filepath.Abs(o.Destination)
	if err != nil {
		return report, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return report, err
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	if source == destination || strings.HasPrefix(destination, source+string(os.PathSeparator)) || strings.HasPrefix(source, destination+string(os.PathSeparator)) {
		return report, fmt.Errorf("migration roots must be separate")
	}
	if _, e := os.Lstat(destination); !os.IsNotExist(e) {
		return report, fmt.Errorf("destination must not exist")
	}
	if o.Boundary == "" {
		o.Boundary = "resource"
	}
	if o.Bucket == "" {
		o.Bucket = "hour"
	}
	stage, err := os.MkdirTemp(parent, ".events-migrate-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(stage)
	taps, err := ListTaps(ctx, source)
	if err != nil {
		return report, err
	}
	var locked []*Manager
	defer func() {
		for _, m := range locked {
			err = errors.Join(err, m.Close())
		}
	}()
	report.Source = source
	for _, tap := range taps {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		old, e := Open(Config{Root: source, Tap: tap, recoverOnly: true})
		if e != nil {
			return report, e
		}
		locked = append(locked, old)
		next, e := Open(Config{Root: stage, Tap: tap})
		if e != nil {
			return report, e
		}
		e = func() error {
			defer next.Close()
			var schemas []*schema
			maximum := map[string]uint32{}
			for _, s := range old.schemas {
				schemas = append(schemas, s)
				family := fmt.Sprintf("%s.v%d", s.definition.Object, s.revision.Version>>20)
				if s.revision.Version > maximum[family] {
					maximum[family] = s.revision.Version
				}
			}
			sort.Slice(schemas, func(i, j int) bool {
				a, b := schemas[i], schemas[j]
				if a.definition.Object == b.definition.Object {
					return a.revision.Version < b.revision.Version
				}
				return a.definition.Object < b.definition.Object
			})
			for _, before := range schemas {
				family := fmt.Sprintf("%s.v%d", before.definition.Object, before.revision.Version>>20)
				if maximum[family]&minorMask == minorMask {
					return ErrLimit
				}
				maximum[family]++
				d := before.definition
				d.Version = maximum[family]
				if o.Timezone != "" {
					d.Timezone = o.Timezone
				}
				d.Storage = &codec.StoragePolicy{Format: 4, Boundary: o.Boundary, Bucket: o.Bucket}
				revision, data, e := d.Prepare()
				if e != nil {
					return e
				}
				if e = next.Install(revision, data); e != nil {
					return e
				}
				after := next.schemas[revision.Digest]
				// Retain every node ordinal, including unused historical nodes and root fallback.
				tr, e := acquireTrie(old.config, before)
				if e != nil {
					return e
				}
				e = func() error {
					defer tr.release()
					if len(tr.nodes) > 1<<16 {
						return fmt.Errorf("source trie exceeds REF16 capacity")
					}
					target := filepath.Join(after.directory(stage, tap), "events.tri")
					f, e := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
					if e != nil {
						return e
					}
					defer f.Close()
					if _, e = io.Copy(f, io.NewSectionReader(tr.file, 0, 1<<63-1)); e != nil {
						return e
					}
					var b [4]byte
					order.PutUint32(b[:], 4)
					if _, e = f.WriteAt(b[:], 4); e != nil {
						return e
					}
					return f.Sync()
				}()
				if e != nil {
					return e
				}
				result := MigratedSchema{Tap: tap, Old: before.revision, New: revision}
				e = old.walkRows(ctx, before, func(p *partition, r row, pop []byte) error {
					fields, e := codec.Decode(pop, codec.CBOR)
					if e != nil {
						return e
					}
					duration, ok := fields[before.duration].(uint64)
					if before.hasDuration && !ok {
						return ErrInvalid
					}
					owner := after.owner(p.trie, r.Leaf)
					out, e := next.partitionAt(after, after.scope(r.Id, owner))
					if e != nil {
						return e
					}
					duplicate, e := out.append(record{Id: r.Id, Leaf: r.Leaf, Prepared: true, Duration: duration, Dynamic: append([]byte(nil), pop...)}, time.Now())
					if e != nil {
						return e
					}
					if duplicate {
						return fmt.Errorf("source contains duplicate UUID")
					}
					addMigrationHash(&result.Checksum, r, pop)
					result.Events++
					return nil
				})
				if e != nil {
					return e
				}
				if e = next.Flush(); e != nil {
					return e
				}
				var count uint64
				var sum [32]byte
				if e = next.walkRows(ctx, after, func(_ *partition, r row, pop []byte) error { count++; addMigrationHash(&sum, r, pop); return nil }); e != nil {
					return e
				}
				if count != result.Events || sum != result.Checksum {
					return fmt.Errorf("migration verification failed for %s/%d", tap, before.revision.Version)
				}
				report.Schemas = append(report.Schemas, result)
			}
			return next.Close()
		}()
		if e != nil {
			return report, e
		}
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, err
	}
	f, err := os.OpenFile(filepath.Join(stage, "migration.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return report, err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return report, err
	}
	if err = syncDirectory(stage); err != nil {
		return report, err
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	// Rename cannot replace an existing nonempty directory. Recheck to reject empty destinations too.
	if _, e := os.Lstat(destination); !os.IsNotExist(e) {
		return report, fmt.Errorf("destination appeared during migration")
	}
	if err = os.Rename(stage, destination); err != nil {
		return report, err
	}
	err = syncDirectory(parent)
	return report, err
}
func addMigrationHash(sum *[32]byte, r row, pop []byte) {
	h := sha256.New()
	h.Write(r.Id[:])
	var leaf [4]byte
	order.PutUint32(leaf[:], r.Leaf)
	h.Write(leaf[:])
	h.Write(pop)
	digest := h.Sum(nil)
	carry := uint16(0)
	for i := 31; i >= 0; i-- {
		carry += uint16(sum[i]) + uint16(digest[i])
		sum[i] = byte(carry)
		carry >>= 8
	}
}
func (m *Manager) walkRows(ctx context.Context, s *schema, visit func(*partition, row, []byte) error) error {
	var scopes []scope
	if s.modern() {
		for _, sc := range m.known[s.revision.Digest] {
			scopes = append(scopes, sc)
		}
	} else {
		base := s.directory(m.config.Root, m.config.Tap)
		e := filepath.WalkDir(base, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() || d.Name() != "log.idx" {
				return nil
			}
			rel, e := filepath.Rel(base, filepath.Dir(path))
			if e != nil {
				return e
			}
			day, e := parseDay(rel)
			if e != nil {
				return e
			}
			scopes = append(scopes, scope{Start: day, End: day.AddDate(0, 0, 1), Dir: rel})
			return nil
		})
		if e != nil {
			return e
		}
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].Dir < scopes[j].Dir })
	for _, sc := range scopes {
		if e := ctx.Err(); e != nil {
			return e
		}
		p, e := m.partitionAt(s, sc)
		if e != nil {
			return e
		}
		for _, run := range p.runs {
			b := make([]byte, run.End-run.Offset-8)
			if _, e = p.idx.ReadAt(b, run.Offset+8); e != nil {
				return e
			}
			var frame [8]byte
			if _, e = p.idx.ReadAt(frame[:], run.Offset); e != nil {
				return e
			}
			if order.Uint32(frame[:]) != uint32(len(b)) || checksum(b) != order.Uint32(frame[4:]) {
				return fmt.Errorf("source checksum mismatch")
			}
			rows, e := p.decodeRun(b)
			if e != nil {
				return e
			}
			pop, e := p.readPopRun(rows)
			if e != nil {
				return e
			}
			for _, r := range rows {
				if e = ctx.Err(); e != nil {
					return e
				}
				if e = visit(p, r, pop[r.Start-rows[0].Start:r.End-rows[0].Start]); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
