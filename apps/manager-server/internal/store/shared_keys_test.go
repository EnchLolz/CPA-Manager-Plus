package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedKeyAccountingSurvivesRestartAndAllowsConcurrentRequests(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sharing.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key := SharedKey{ID: "test", Name: "Friend", Models: []string{"model"}, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), MaxTokens: 10}
	if err := st.CreateSharedKey(ctx, key, "hash"); err != nil {
		t.Fatal(err)
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	var acquired atomic.Int64
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := st
			if i%2 == 0 {
				db = other
			}
			ok, err := db.AcquireSharedKey(ctx, key.ID)
			if err != nil {
				t.Error(err)
			}
			if ok {
				acquired.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if acquired.Load() != 16 {
		t.Fatalf("concurrent acquisitions=%d", acquired.Load())
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if ok, err := st.AcquireSharedKey(ctx, key.ID); err != nil || !ok {
		t.Fatalf("restart left the key locked: %v %v", ok, err)
	}
	if err := st.FinishSharedRequest(ctx, key.ID, 10, 25); err != nil {
		t.Fatal(err)
	}
	if ok, err := other.AcquireSharedKey(ctx, key.ID); err != nil || ok {
		t.Fatalf("persisted allowance not enforced: %v %v", ok, err)
	}
	got, err := st.FindSharedKey(ctx, "hash")
	if err != nil || got.UsedTokens != 10 || got.UsedCostMicros != 25 || got.InFlight {
		t.Fatalf("accounting=%+v err=%v", got, err)
	}
}
