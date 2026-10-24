package cache

import (
	"fmt"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

// Metadata-only control reproduces the previous retained Go directories. Both
// variants borrow the same native payload, use one object with N revisions,
// and retain UUID ordering. This isolates metadata rather than disk or PIL work.
type oldResident struct {
	raw     []byte
	object  [16]byte
	indexed bool
	row     uint64
}
type oldLiveControl struct {
	pending   []pending
	ids       map[[16]byte][]byte
	objects   map[string][]pending
	bounds    map[[16]byte]revisionSpan
	revisions map[collectionRevision][16]byte
	hot       map[[16]byte]oldResident
	oldest    [][16]byte
}

func newOldLiveControl() *oldLiveControl {
	return &oldLiveControl{ids: map[[16]byte][]byte{}, objects: map[string][]pending{}, bounds: map[[16]byte]revisionSpan{}, revisions: map[collectionRevision][16]byte{}, hot: map[[16]byte]oldResident{}}
}
func (c *oldLiveControl) add(p pending, key collectionRevision) {
	c.pending = append(c.pending, p)
	c.ids[p.id] = p.frame
	c.objects[string(key.Object[:])] = append(c.objects[string(key.Object[:])], p)
	c.revisions[key] = p.id
	v, ok := c.bounds[key.Object]
	if !ok {
		v.Low = key.Revision
	}
	v.High = key.Revision
	c.bounds[key.Object] = v
	c.hot[p.id] = oldResident{raw: p.frame}
	c.oldest = append(c.oldest, p.id)
}

func BenchmarkLiveMetadata(b *testing.B) {
	for _, n := range []int{10000, 100000} {
		for _, native := range []bool{false, true} {
			b.Run(fmt.Sprintf("records=%d/native=%t", n, native), func(b *testing.B) {
				pool := newNativeMemory(1 << 20)
				defer pool.close()
				buf := pool.buffer()
				defer buf.close()
				raw, e := buf.resize(128)
				if e != nil {
					b.Fatal(e)
				}
				at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
				var object [16]byte
				object[0] = 42
				b.StopTimer()
				var retained int64
				for run := 0; run < b.N; run++ {
					runtime.GC()
					var before, after runtime.MemStats
					runtime.ReadMemStats(&before)
					var old *oldLiveControl
					var live *nativeLive
					if native {
						live = newNativeLive(n + 1)
					} else {
						old = newOldLiveControl()
					}
					b.StartTimer()
					for i := 0; i < n; i++ {
						var id [16]byte
						ms := uint64(at.UnixMilli()) + uint64(i)
						for j := 5; j >= 0; j-- {
							id[j] = byte(ms)
							ms >>= 8
						}
						id[6] = 0x70
						id[8] = 0x80
						p := pending{id, raw}
						key := collectionRevision{object, uint64(i)}
						if native {
							if !live.insert(p, &key, ProvisionalRow, true, at.Add(-HotWindow).UnixMilli()) {
								b.Fatal("native limit")
							}
						} else {
							old.add(p, key)
						}
					}
					b.StopTimer()
					runtime.GC()
					runtime.ReadMemStats(&after)
					retained += int64(after.HeapAlloc) - int64(before.HeapAlloc)
					if native {
						if live.count(0) != n {
							b.Fatal("lost entries")
						}
						live.close()
					} else {
						if len(old.ids) != n {
							b.Fatal("lost entries")
						}
						runtime.KeepAlive(old)
					}
				}
				b.ReportMetric(float64(retained)/float64(b.N), "Go-retained-B")
				b.ReportMetric(float64(retained)/float64(b.N*n), "Go-B/record")
				b.ReportMetric(float64(unsafe.Sizeof(pending{})), "Go-pending-row-B")
			})
		}
	}
}

func BenchmarkSealPhases(b *testing.B) {
	for _, branch := range []Branch{Read, Write} {
		for _, n := range []int{1000, 10000} {
			b.Run(fmt.Sprintf("%s/records=%d", branch.Name(), n), func(b *testing.B) {
				b.StopTimer()
				l := testLayout(b, false)
				at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
				frames := make([][]byte, n)
				for i := range frames {
					raw := testFrame(b, l, at.Add(time.Duration(i)*time.Millisecond), uint32(i+1), i%3 != 0)
					var f Frame
					if e := l.Scan(raw, &f); e != nil {
						b.Fatal(e)
					}
					f.Values[l.Object] = testObject(uint32(i / 10))
					f.Values[l.Revision] = Uint(uint64(i%10), 8)
					var e error
					frames[i], e = l.Encode(nil, f.Values)
					if e != nil {
						b.Fatal(e)
					}
				}
				var work, publish, wait, total time.Duration
				for run := 0; run < b.N; run++ {
					now := at.Add(time.Minute)
					scheduler := NewSealScheduler(1)
					s, e := Open(Config{Root: b.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: branch == Write, Sealer: scheduler, Now: func() time.Time { return now }})
					if e != nil {
						b.Fatal(e)
					}
					if count, e := s.AppendBatch(branch, frames); e != nil || count != n {
						b.Fatal(count, e)
					}
					now = at.Add(80 * time.Minute)
					b.StartTimer()
					started := time.Now()
					e = s.Flush()
					elapsed := time.Since(started)
					b.StopTimer()
					if e != nil {
						b.Fatal(e)
					}
					stats := s.SealingStats()
					work += stats.WorkTime
					publish += stats.PublishTime
					wait += stats.PublishWait
					total += elapsed
					if s.branches[branch].live.count(0) != 0 || len(s.branches[branch].runs) != 1 {
						b.Fatal("incomplete seal")
					}
					if e = s.Close(); e != nil {
						b.Fatal(e)
					}
				}
				b.ReportMetric(float64(work.Nanoseconds())/float64(b.N), "outside-lock-ns")
				b.ReportMetric(float64(publish.Nanoseconds())/float64(b.N), "publication-ns")
				b.ReportMetric(float64(wait.Nanoseconds())/float64(b.N), "publication-wait-ns")
				b.ReportMetric(float64((total-work-wait).Nanoseconds())/float64(b.N), "total-locked-ns")
			})
		}
	}
}
