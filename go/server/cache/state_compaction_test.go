package cache

import (
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func TestStateCompactionPreservesTokensAndContinuesSequence(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: testLayout(t, false)}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	// Build a durable, valid repeated-invalidation history without paying a
	// filesystem sync per fixture record. Recovery is the production reader.
	var journal []byte
	for seq := uint64(1); seq <= 100000; seq++ {
		id := uint64(42)
		if seq == 2 {
			id = 43
		}
		record := invalidation{Sequence: seq, Objects: [][]byte{testObject(uint32(id)).Bytes}, Collection: seq == 3}
		raw, err := encoder.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		journal = order.AppendUint32(journal, uint32(len(raw)))
		journal = order.AppendUint32(journal, crc32.Checksum(raw, checksum))
		journal = append(journal, raw...)
	}
	if _, err = s.state.file.WriteAt(journal, headerSize); err != nil {
		t.Fatal(err)
	}
	if err = s.state.file.Sync(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Token(testObject(43))
	if err != nil {
		t.Fatal(err)
	}
	if s.state.journalBytes <= s.state.compactionThreshold() {
		t.Fatal("fixture did not reach automatic threshold")
	}
	if err = s.invalidate([][]byte{testObject(42).Bytes}, false); err != nil {
		t.Fatal(err)
	}
	if s.state.journalBytes > 1024 {
		t.Fatal("state did not compact", s.state.journalBytes)
	}
	t.Logf("100,000-record journal: %d bytes; checkpoint after next invalidation: %d bytes", headerSize+len(journal), s.state.journalBytes)
	after, err := s.Token(testObject(43))
	if err != nil || after != before {
		t.Fatal("unrelated token changed", before, after, err)
	}
	changed, _ := s.Token(testObject(42))
	if changed.Object != 100001 || changed.Collection != 3 {
		t.Fatal(changed)
	}
	if err = s.CompactState(); err != nil {
		t.Fatal(err)
	}
	if err = s.invalidate([][]byte{testObject(44).Bytes}, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.collection, ".collection-state-interrupted")
	if err = os.WriteFile(path, []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Token(testObject(43))
	if got != before {
		t.Fatal("checkpoint replay changed token", got)
	}
	got, _ = s.Token(testObject(42))
	if got != changed {
		t.Fatal("checkpoint lost latest object", got)
	}
	got, _ = s.Token(testObject(44))
	if got.Object != 100002 {
		t.Fatal("post-checkpoint delta lost", got)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("orphan checkpoint was not removed", err)
	}
}

func TestIncompleteStateCheckpointCannotReopenAsEmpty(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: testLayout(t, false)}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.invalidate([][]byte{testObject(42).Bytes}, false); err != nil {
		t.Fatal(err)
	}
	if err = s.CompactState(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.collection, "collection.state")
	s.Close()
	if err = os.Truncate(path, headerSize+2); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(cfg); err == nil {
		s.Close()
		t.Fatal("missing checkpoint accepted as fresh state")
	}
}
