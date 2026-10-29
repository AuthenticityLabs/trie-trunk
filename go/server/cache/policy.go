package cache

import (
	"bytes"
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Retention struct {
	Read  string `toml:"read"`
	Write string `toml:"write"`
	Error string `toml:"error"`
	Debug string `toml:"debug"`
	Trace string `toml:"trace"`
}

func (r Retention) Durations() (map[Branch]time.Duration, error) {
	out := map[Branch]time.Duration{}
	for i, raw := range []string{r.Read, r.Write, r.Error, r.Debug, r.Trace} {
		if raw == "" {
			continue
		}
		if strings.HasSuffix(raw, "d") {
			raw = strings.TrimSuffix(raw, "d") + "h"
			d, e := time.ParseDuration(raw)
			if e != nil || d <= 0 || d > (1<<63-1)/24 {
				return nil, fmt.Errorf("invalid retention")
			}
			out[Branch(i)] = 24 * d
		} else {
			d, e := time.ParseDuration(raw)
			if e != nil || d <= 0 {
				return nil, fmt.Errorf("invalid retention")
			}
			out[Branch(i)] = d
		}
	}
	return out, nil
}

type ReplicationPolicy struct {
	IndexPeriod     string `toml:"index_period"`
	Data            string `toml:"data"`
	ProvisionalTail string `toml:"provisional_tail"`
}
type PACPolicy struct {
	GzipLevel *int `toml:"gzip_level"`
}

func (p PACPolicy) level() int {
	if p.GzipLevel != nil {
		return *p.GzipLevel
	}
	return 1
}

// SummaryPolicy applies to completed daily aggregate packets, independently of event PACs.
type SummaryPolicy struct {
	GzipLevel *int `toml:"gzip_level"`
}

func (p SummaryPolicy) level() int {
	if p.GzipLevel != nil {
		return *p.GzipLevel
	}
	return 6
}

type Policy struct {
	SearchIndex       *bool             `toml:"search_index"`
	Staging           *StagingLimits    `toml:"staging"`
	ObjectIndex       *bool             `toml:"object_index"`
	CacheInvalidation *bool             `toml:"cache_invalidation"`
	Retention         Retention         `toml:"retention"`
	Replication       ReplicationPolicy `toml:"replication"`
	PAC               PACPolicy         `toml:"pac"`
	Summary           SummaryPolicy     `toml:"summary"`
}
type Configuration struct {
	Version   uint32            `toml:"version"`
	Timezone  string            `toml:"timezone"`
	Defaults  Policy            `toml:"defaults"`
	Resources map[string]Policy `toml:"resources"`
}

func (p Policy) validate() error {
	if p.Summary.level() < 1 || p.Summary.level() > 9 {
		return fmt.Errorf("summary.gzip_level must be between 1 and 9")
	}
	if p.PAC.level() < 1 || p.PAC.level() > 9 {
		return fmt.Errorf("pac.gzip_level must be between 1 and 9")
	}
	if p.Staging != nil {
		if e := p.Staging.validate(); e != nil {
			return e
		}
	}
	if _, e := p.Retention.Durations(); e != nil {
		return e
	}
	r := p.Replication
	if !validIndexPeriod(r.IndexPeriod) || r.Data != "" && r.Data != "lazy" {
		return fmt.Errorf("unsupported replication policy")
	}
	if r.ProvisionalTail != "" {
		d, e := time.ParseDuration(r.ProvisionalTail)
		if e != nil || d < 15*time.Minute {
			return fmt.Errorf("provisional tail must be at least 15m")
		}
	}
	return nil
}
func ParseConfiguration(raw []byte) (Configuration, error) {
	var c Configuration
	if len(raw) > 1<<20 {
		return c, fmt.Errorf("configuration too large")
	}
	e := toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(&c)
	if e != nil {
		return c, e
	}
	if c.Version != 1 {
		return c, fmt.Errorf("unsupported configuration version")
	}
	if _, e = time.LoadLocation(c.Timezone); e != nil {
		return c, e
	}
	if e = c.Defaults.validate(); e != nil {
		return c, e
	}
	for key, p := range c.Resources {
		if splitBinding(key+"/0") == nil {
			return c, fmt.Errorf("invalid resource policy key")
		}
		if e = p.validate(); e != nil {
			return c, e
		}
	}
	return c, nil
}
func (c Configuration) Resolve(key string) Policy {
	return mergePolicy(c.Defaults, c.Resources[key])
}

func mergePolicy(p, q Policy) Policy {
	if q.Summary.GzipLevel != nil {
		p.Summary.GzipLevel = q.Summary.GzipLevel
	}
	if q.SearchIndex != nil {
		p.SearchIndex = q.SearchIndex
	}
	if q.PAC.GzipLevel != nil {
		p.PAC.GzipLevel = q.PAC.GzipLevel
	}
	if q.Staging != nil {
		p.Staging = q.Staging
	}
	if q.ObjectIndex != nil {
		p.ObjectIndex = q.ObjectIndex
	}
	if q.CacheInvalidation != nil {
		p.CacheInvalidation = q.CacheInvalidation
	}
	from := []string{q.Retention.Read, q.Retention.Write, q.Retention.Error, q.Retention.Debug, q.Retention.Trace}
	to := []*string{&p.Retention.Read, &p.Retention.Write, &p.Retention.Error, &p.Retention.Debug, &p.Retention.Trace}
	for i, v := range from {
		if v != "" {
			*to[i] = v
		}
	}
	if q.Replication.IndexPeriod != "" {
		p.Replication.IndexPeriod = q.Replication.IndexPeriod
	}
	if q.Replication.Data != "" {
		p.Replication.Data = q.Replication.Data
	}
	if q.Replication.ProvisionalTail != "" {
		p.Replication.ProvisionalTail = q.Replication.ProvisionalTail
	}
	return p
}
func (s *Store) loadPolicy() error {
	path := filepath.Join(s.root, "registry.cfg")
	raw, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		// Keep built-in defaults implicit so a newly created cache does not
		// shadow an explicitly configured ancestor with generated values.
		raw = []byte(fmt.Sprintf("version = 1\ntimezone = %q\n", s.layout.Definition.Resource.Timezone))
		if s.config.ObjectIndex {
			n := s.layout.Definition.Resource
			key := fmt.Sprintf("%s/%s.v%d", n.Service, n.Resource, n.Major)
			raw = append(raw, []byte(fmt.Sprintf("\n[resources.%q]\nobject_index = true\n", key))...)
		}
		if e = replace(path, raw); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	c, e := ParseConfiguration(raw)
	if e != nil {
		return e
	}
	n := s.layout.Definition.Resource
	p, branchPolicies, e := s.hierarchicalPolicy(c, fmt.Sprintf("%s/%s.v%d", n.Service, n.Resource, n.Major))
	if e != nil {
		return e
	}
	s.policy = p
	s.branchPolicies = branchPolicies
	s.config.ObjectIndex = s.requestedObjectIndex
	if p.ObjectIndex != nil {
		s.config.ObjectIndex = *p.ObjectIndex
	}
	return nil
}

// ReloadPolicy validates the entire replacement before changing the active
// policy. Enabling the optional object index deliberately backfills retained data.
func (s *Store) ReloadPolicy() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	old, oldBranches, enabled := s.policy, s.branchPolicies, s.config.ObjectIndex
	if e := s.loadPolicy(); e != nil {
		return e
	}
	if s.config.ObjectIndex && s.objects == nil {
		if e := s.openObjects(); e != nil {
			s.policy = old
			s.branchPolicies = oldBranches
			s.config.ObjectIndex = enabled
			return e
		}
		if b := s.branches[Write]; b != nil {
			if e := s.indexObjects(b); e != nil {
				return s.fail(e)
			}
		}
	}

	return nil
}
