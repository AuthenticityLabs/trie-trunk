package cache

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestNativeSearchTypedBoundaryAndConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typed.search")
	r := searchRequest{Op: "append", Path: path, Binding: "native-test", Fields: []string{"e", "u", "i", "f", "b", "m"}, Tail: 2, Docs: []searchDoc{
		{Row: 1, SourceRow: 1, Cells: []searchCell{
			{K: 0, State: 2, E: []string{"abc"}}, {K: 1, State: 2, U: []uint64{^uint64(0)}}, {K: 2, State: 2, I: []int64{-1 << 63}},
			{K: 3, State: 2, F: []float64{1.25}}, {K: 4, State: 2, B: []bool{true, false}}, {K: 5, State: 2, U: []uint64{7}, T: []string{"東京 café"}},
		}},
	}}
	_, e := searchCall(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	defer searchCall(context.Background(), searchRequest{Op: "release", Path: path})
	code := uint64(7)
	r.Op = "query"
	r.Docs = nil
	r.Ranges = [][2]uint64{{1, 2}}
	r.Filters = []searchFilter{{K: 0, Kind: "e", Op: "==", Value: "abc"}, {K: 1, Kind: "u", Op: "==", Value: "18446744073709551615"}, {K: 2, Kind: "i", Op: "==", Value: "-9223372036854775808"}, {K: 3, Kind: "f", Op: "==", Value: "1.25"}, {K: 4, Kind: "b", Op: "==", Value: "false"}, {K: 5, Kind: "m", Op: "==", Value: "東京 café", Code: &code}}
	// Exercise both uncancellable and cancellable calls through the typed ABI.
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				ctx, cancel := context.WithCancel(context.Background())
				reply, err := searchCall(ctx, r)
				cancel()
				if err != nil || !reflect.DeepEqual(reply.Rows, []uint64{1}) {
					t.Errorf("typed concurrent result %v: %v", reply.Rows, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := searchCall(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	reply, err := searchCall(context.Background(), r)
	if err != nil || !reflect.DeepEqual(reply.Rows, []uint64{1}) {
		t.Fatalf("reader after cancellation: %v %v", reply.Rows, err)
	}
	// Unknown operations return a native error without crashing or invalidating the index.
	bad := r
	bad.Op = "invalid"
	if _, err = searchCall(context.Background(), bad); err == nil {
		t.Fatal("expected native error")
	}
	if _, err = searchCall(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	// Drop native handles and reopen the persisted checkpoint through the same ABI.
	if _, err = searchCall(context.Background(), searchRequest{Op: "release", Path: path}); err != nil {
		t.Fatal(err)
	}
	reply, err = searchCall(context.Background(), r)
	if err != nil || !reflect.DeepEqual(reply.Rows, []uint64{1}) {
		t.Fatalf("reopened reader: %v %v", reply.Rows, err)
	}
}
