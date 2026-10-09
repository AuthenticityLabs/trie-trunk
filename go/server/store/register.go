package store

import "trie-trunk/go/codec"

func (m *Manager) Register(d codec.Definition) (codec.Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return codec.Revision{}, err
	}

	if d.Storage == nil {
		d.Storage = &codec.StoragePolicy{Format: 4, Boundary: "resource", Bucket: "hour"}
	}
	if d.Object == "" {
		d.Object = "logs"
	}
	major := d.Version >> 20
	if major == 0 {
		major = 1
	}
	base := major << 20
	d.Version = base
	wanted, _, err := d.Prepare()
	if err != nil {
		return codec.Revision{}, err
	}
	last := base
	found := false
	for _, s := range m.schemas {
		if s.definition.Object != d.Object || s.revision.Version>>20 != major {
			continue
		}
		version := s.revision.Version
		found = true
		if version > last {
			last = version
		}
		existing := s.definition
		existing.Version = base
		rev, _, err := existing.Prepare()
		if err != nil {
			return codec.Revision{}, err
		}
		if rev.Digest == wanted.Digest {
			return s.revision, nil
		}
	}
	if last&((1<<20)-1) == (1<<20)-1 {
		return codec.Revision{}, ErrLimit
	}
	d.Version = last
	if found {
		d.Version++
	}
	rev, data, err := d.Prepare()
	if err != nil {
		return codec.Revision{}, err
	}
	if len(data) > codec.MaxFramePayload {
		return codec.Revision{}, codec.ErrFrameSize
	}

	d, err = codec.ParseDefinition(data, rev)
	if err != nil {
		return codec.Revision{}, err
	}
	s, err := compile(d, rev)
	if err != nil {
		return codec.Revision{}, err
	}
	if err = m.install(s, data); err != nil {
		return codec.Revision{}, err
	}
	return rev, nil
}
