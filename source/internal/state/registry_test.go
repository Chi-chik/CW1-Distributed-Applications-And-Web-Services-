package state

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func clock(start time.Time) (func() time.Time, func(time.Duration)) {
	var mu sync.Mutex
	t := start
	return func() time.Time { mu.Lock(); defer mu.Unlock(); return t },
		func(d time.Duration) { mu.Lock(); t = t.Add(d); mu.Unlock() }
}

func peer(id string) Peer { return Peer{ID: id, Addrs: Addresses{GRPC: "127.0.0.1:9001"}} }

func TestPeersListedInDeterministicOrder(t *testing.T) {
	r := NewRegistry("self", nil)
	for _, id := range []string{"node3", "node1", "node2"} {
		if _, err := r.AddPeer(peer(id)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ { // map order is randomised; repeat to catch flakiness
		got := r.Peers()
		if got[0].ID != "node1" || got[1].ID != "node2" || got[2].ID != "node3" {
			t.Fatalf("not sorted: %v", got)
		}
	}
}

func TestPeerRejections(t *testing.T) {
	cases := []struct {
		name string
		p    Peer
		want error
	}{
		{"bad id", Peer{ID: "Bad_ID", Addrs: Addresses{GRPC: "h:1"}}, ErrInvalid},
		{"no addrs", Peer{ID: "n2"}, ErrInvalid},
		{"port zero", Peer{ID: "n2", Addrs: Addresses{TCP: "h:0"}}, ErrInvalid},
		{"no host", Peer{ID: "n2", Addrs: Addresses{TCP: ":9000"}}, ErrInvalid},
		{"self", peer("self"), ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry("self", nil)
			if _, err := r.AddPeer(tc.p); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if len(r.Peers()) != 0 {
				t.Fatal("rejected peer was stored")
			}
		})
	}
}

func TestDuplicatePeerIsConflictAndOriginalKept(t *testing.T) {
	r := NewRegistry("self", nil)
	first := peer("n1")
	r.AddPeer(first)
	dup := Peer{ID: "n1", Addrs: Addresses{GRPC: "10.0.0.9:1"}}
	if _, err := r.AddPeer(dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	got, _ := r.Peer("n1")
	if got.Addrs.GRPC != first.Addrs.GRPC {
		t.Fatal("duplicate overwrote original")
	}
}

func TestPeerTableIsBounded(t *testing.T) {
	r := NewRegistry("self", nil)
	for i := 0; i < MaxPeers; i++ {
		if _, err := r.AddPeer(peer(fmt.Sprintf("p%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.AddPeer(peer("one-too-many")); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict at limit, got %v", err)
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	r := NewRegistry("self", nil)
	r.AddPeer(peer("n1"))
	snap := r.Snapshot()
	snap.Peers[0].ID = "tampered"
	if got, _ := r.Peer("n1"); got.ID != "n1" {
		t.Fatal("mutating a snapshot changed shared state")
	}
}

func TestPeerExpiry(t *testing.T) {
	now, advance := clock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	r := NewRegistry("self", now)
	r.AddPeer(peer("static"))
	r.AddPeer(peer("dynamic"))
	r.TouchPeer("dynamic", 10*time.Second)

	advance(5 * time.Second)
	if gone := r.ExpirePeers(); len(gone) != 0 {
		t.Fatalf("expired too early: %v", gone)
	}
	advance(6 * time.Second)
	if gone := r.ExpirePeers(); len(gone) != 1 || gone[0] != "dynamic" {
		t.Fatalf("want [dynamic], got %v", gone)
	}
	if _, err := r.Peer("static"); err != nil {
		t.Fatal("static peer must never expire")
	}
}

func TestFileValidation(t *testing.T) {
	r := NewRegistry("self", nil)
	bad := []FileMeta{
		{Name: "../etc/passwd", Size: 1, SHA256: hashA},
		{Name: ".hidden", Size: 1, SHA256: hashA},
		{Name: "a..b", Size: 1, SHA256: hashA},
		{Name: "dir/file", Size: 1, SHA256: hashA},
		{Name: "ok.txt", Size: -1, SHA256: hashA},
		{Name: "ok.txt", Size: MaxFileSize + 1, SHA256: hashA},
		{Name: "ok.txt", Size: 1, SHA256: "ABC"},
	}
	for _, f := range bad {
		if _, err := r.AddFile(f); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: want ErrInvalid, got %v", f, err)
		}
	}
	got, err := r.AddFile(FileMeta{Name: "notes-1.txt", Size: 12, SHA256: hashA})
	if err != nil || got.Owner != "self" {
		t.Fatalf("valid file rejected or owner unset: %+v %v", got, err)
	}
}

// Many goroutines add, read and snapshot at once. Run with -race: the
// detector, not just the final count, is what proves there is no corruption.
func TestConcurrentAccessKeepsStateConsistent(t *testing.T) {
	r := NewRegistry("self", nil)
	const workers, perWorker = 15, 16 // 240 + "shared" stays under MaxPeers

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := fmt.Sprintf("w%d-p%d", w, i)
				if _, err := r.AddPeer(peer(id)); err != nil {
					t.Errorf("add %s: %v", id, err)
				}
				_ = r.Snapshot()
				_, _ = r.AddPeer(peer("shared")) // contended duplicate
			}
		}(w)
	}
	wg.Wait()

	got := r.Peers()
	want := workers*perWorker + 1 // +1 for "shared", inserted exactly once
	if len(got) != want {
		t.Fatalf("got %d peers, want %d", len(got), want)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID >= got[i].ID {
			t.Fatal("snapshot not strictly sorted")
		}
	}
}
