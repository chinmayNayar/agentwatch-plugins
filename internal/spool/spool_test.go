package spool

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type body struct {
	N  int    `json:"n"`
	ID string `json:"event_id"`
}

func (b body) EventID() string { return b.ID }

func TestAppendFlushRetryDedupe(t *testing.T) {
	s := New(t.TempDir())
	for i := 0; i < 5; i++ {
		if err := s.Append("ingest", body{N: i, ID: "e" + string(rune('0'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	// A retried batch: the same event id twice is sent once.
	_ = s.Append("ingest", body{N: 9, ID: "e0"})
	var got []string
	down := true
	send := func(r Record) SendResult {
		if down && r.ID == "e2" {
			return Retry
		}
		got = append(got, r.ID)
		return Sent
	}
	sent, _, _ := s.Flush(context.Background(), send)
	if sent != 2 || len(got) != 2 {
		t.Fatalf("first pass sent %d (%v)", sent, got)
	}
	if s.Pending() != 4 { // e2, e3, e4, e0-dup kept for later
		t.Fatalf("pending %d", s.Pending())
	}
	down = false
	got = nil
	sent, _, _ = s.Flush(context.Background(), send)
	// Within one pass a duplicate id is sent once; across passes the server
	// dedupes by event_id, so the duplicate goes out with the same id.
	if sent != 4 || got[3] != "e0" {
		t.Fatalf("second pass sent %d: %v", sent, got)
	}
	if s.Pending() != 0 {
		t.Fatalf("left %d", s.Pending())
	}
}

func TestPermanentFailuresGoToDead(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	_ = s.Append("ingest", body{N: 1, ID: "a"})
	_, dead, _ := s.Flush(context.Background(), func(Record) SendResult { return Permanent })
	if dead != 1 || s.Pending() != 0 {
		t.Fatalf("dead=%d pending=%d", dead, s.Pending())
	}
	if _, err := os.Stat(filepath.Join(dir, "dead", "dead.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestFullSpoolDropsNewAndCounts(t *testing.T) {
	old := MaxBytes
	MaxBytes = 600
	defer func() { MaxBytes = old }()
	s := New(t.TempDir())
	var dropped int
	for i := 0; i < 20; i++ {
		if err := s.Append("ingest", body{N: i, ID: NewID()}); err == ErrFull {
			dropped++
		}
	}
	if dropped == 0 || s.Dropped() != dropped {
		t.Fatalf("dropped %d counted %d", dropped, s.Dropped())
	}
	s.ResetDropped(dropped)
	if s.Dropped() != 0 {
		t.Fatal("reset failed")
	}
}

func TestOneFlusherAtATime(t *testing.T) {
	s := New(t.TempDir())
	unlock, ok := s.Lock()
	if !ok {
		t.Fatal("first lock failed")
	}
	if _, ok2 := s.Lock(); ok2 {
		t.Fatal("second lock succeeded")
	}
	unlock()
	if _, ok3 := s.Lock(); !ok3 {
		t.Fatal("lock not released")
	}
}

func TestStaleLockIsTakenOver(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	p := filepath.Join(dir, ".lock")
	_ = os.WriteFile(p, []byte("1"), 0o600)
	old := time.Now().Add(-10 * time.Minute)
	_ = os.Chtimes(p, old, old)
	if _, ok := s.Lock(); !ok {
		t.Fatal("stale lock not taken over")
	}
}

func TestConcurrentAppendsKeepWholeLines(t *testing.T) {
	s := New(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _ = s.Append("ingest", body{N: i, ID: NewID()}) }(i)
	}
	wg.Wait()
	n := 0
	_, _, _ = s.Flush(context.Background(), func(Record) SendResult { n++; return Sent })
	if n != 50 {
		t.Fatalf("sent %d of 50 (a torn line would be dropped as invalid)", n)
	}
}
