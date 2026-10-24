package cache

// Per-branch publication gates and bounded background catch-up lifecycle.
import (
	"context"
	"encoding/hex"
	"log"
	"sync"
	"time"
)

// Catch-up and checkpoint publication are serialized, so concurrent views cannot
// insert the same logical row twice. Reads still share the persistent Rust reader.
type searchSerial struct{ gate chan struct{} }

func (s *searchSerial) LockContext(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *searchSerial) Lock()   { _ = s.LockContext(context.Background()) }
func (s *searchSerial) Unlock() { <-s.gate }

func (s *Store) searchGate(branch Branch) *searchSerial {
	s.searchOnce.Do(func() {
		for i := range s.searchGates {
			s.searchGates[i].gate = make(chan struct{}, 1)
		}
	})
	return &s.searchGates[branch]
}

type searchJob struct {
	s      *Store
	branch Branch
}

var searchJobs = make(chan searchJob, 8)
var searchWorker sync.Once

func (s *Store) queueSearch(branch Branch) {
	enabled := s.policyFor(branch).SearchIndex
	if enabled != nil && !*enabled {
		return
	}
	if s.searchContext == nil {
		s.searchContext, s.searchCancel = context.WithCancel(context.Background())
	}
	searchWorker.Do(func() {
		go func() {
			for job := range searchJobs {
				func() {
					defer job.s.searchWG.Done()
					ctx, cancel := context.WithTimeout(job.s.searchContext, 2*time.Minute)
					defer cancel()
					v, e := job.s.indexSnapshot(job.branch)
					if e != nil {
						return
					}
					defer v.close()
					gate := job.s.searchGate(job.branch)
					if e = gate.LockContext(ctx); e != nil {
						return
					}
					_, e = v.catchupSearch(ctx, v.searchRequest())
					if e == nil {
						e = v.catchupLive(ctx, v.searchRequest())
					}
					if e == nil {
						r := v.searchRequest()
						r.Op = "live_prune"
						for _, p := range v.pending {
							r.IDs = append(r.IDs, hex.EncodeToString(p.id[:]))
						}
						_, e = searchCall(ctx, r)
					}
					gate.Unlock()
					if e != nil && ctx.Err() == nil {
						log.Printf("search index deferred (%s): %v", job.branch.Name(), e)
					}
				}()
			}
		}()
	})
	s.searchWG.Add(1)
	select {
	case searchJobs <- searchJob{s, branch}:
	default:
		s.searchWG.Done()
	} // Overflow is repaired from the canonical checkpoint, never loses events.
}

// Stop background publication before releasing source ownership. Tantivy merge
// threads must also finish before callers can remove a disposable cache directory.
func (s *Store) finishSearch(touched bool) {
	s.searchWG.Wait()
	if !touched {
		return
	}
	for branch := Read; branch <= Trace; branch++ {
		gate := s.searchGate(branch)
		gate.Lock()
		defer gate.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, e := searchCall(ctx, searchRequest{Op: "release", Path: s.dir, Fields: []string{}})
	if e != nil {
		log.Printf("search index release: %v", e)
	}
}
