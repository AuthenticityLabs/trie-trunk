package cache

import (
	"sync"
	"time"
)

// SealScheduler bounds hour packing and canonical file writes across stores.
// Acquire only after releasing the writer lock. Release the permit before
// acquiring that lock again to publish the completed hour.
type SealScheduler struct {
	permits chan struct{}
	mu      sync.Mutex
	stats   SealStats
}
type SealStats struct {
	Workers, Active, Waiting                 int
	Attempts                                 uint64
	QueueTime, WorkTime, MaxWorkTime         time.Duration
	PublishWait, PublishTime, MaxPublishTime time.Duration
	OverWindow                               uint64
	MaxOverrun                               time.Duration
}

func NewSealScheduler(workers int) *SealScheduler {
	if workers < 1 {
		workers = 1
	}
	return &SealScheduler{permits: make(chan struct{}, workers), stats: SealStats{Workers: workers}}
}

var defaultSealScheduler = NewSealScheduler(1)

func (s *SealScheduler) Stats() SealStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}
func (s *SealScheduler) acquire() func() {
	queued := time.Now()
	s.mu.Lock()
	s.stats.Waiting++
	s.mu.Unlock()
	s.permits <- struct{}{}
	started := time.Now()
	s.mu.Lock()
	s.stats.Waiting--
	s.stats.Active++
	s.stats.QueueTime += started.Sub(queued)
	s.mu.Unlock()
	return func() {
		elapsed := time.Since(started)
		s.mu.Lock()
		s.stats.Active--
		s.stats.Attempts++
		s.stats.WorkTime += elapsed
		s.stats.MaxWorkTime = max(s.stats.MaxWorkTime, elapsed)
		s.mu.Unlock()
		<-s.permits
	}
}
func (s *Store) SealingStats() SealStats { return s.config.Sealer.Stats() }

// Hour H closes at H+1h and becomes sealable after the 15-minute tail.
// Its ordinary target is H+2h, leaving 45 minutes for packing and publication.
func (s *SealScheduler) observeWindow(hourEnd, finished time.Time) {
	over := finished.Sub(hourEnd.Add(time.Hour))
	if over <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.OverWindow++
	s.stats.MaxOverrun = max(s.stats.MaxOverrun, over)
}

func (s *SealScheduler) observePublication(wait, work time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.PublishWait += wait
	s.stats.PublishTime += work
	s.stats.MaxPublishTime = max(s.stats.MaxPublishTime, work)
}
