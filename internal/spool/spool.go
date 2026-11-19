// Package spool is the sensor's local outbox. A hook appends one JSON line
// and returns; a detached `agentwatch flush` process sends the lines later.
// Reporting therefore never blocks the agent, and survives the API being
// down or the client killing async hooks at teardown (Claude Code -p).
//
// Layout under ~/.agentwatch/spool/:
//
//	active.jsonl   appended to by hooks (O_APPEND, one write per line)
//	seg-*.jsonl    rotated by the flusher, sent oldest first
//	dead/          lines the API refused (4xx other than 408/429)
//	dropped        count of lines dropped because the spool was full
//	.lock          held by the one running flusher
//
// Bounds: MaxBytes in total. When full, new lines are dropped (not old ones:
// the oldest unsent events are the ones closest to an incident) and counted;
// the count is reported in the next heartbeat.
package spool

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	MaxBytes int64 = 20 << 20
	MaxLine        = 256 << 10
	lockTTL        = 2 * time.Minute
)

// Record is one queued item.
type Record struct {
	ID   string          `json:"id"`
	TS   int64           `json:"ts"`
	Type string          `json:"type"` // ingest | event | heartbeat
	Body json.RawMessage `json:"body"`
}

// Spool is a directory-backed outbox.
type Spool struct {
	Dir string
	mu  sync.Mutex
}

func New(dir string) *Spool { return &Spool{Dir: dir} }

// NewID is a random 128-bit id (dedupe key on the server).
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ErrFull means the record was dropped and counted.
var ErrFull = errors.New("spool full; event dropped")

// Append queues one record. It never blocks on the network.
func (s *Spool) Append(typ string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	rec := Record{ID: NewID(), TS: time.Now().UnixMilli(), Type: typ, Body: raw}
	if b, ok := body.(interface{ EventID() string }); ok && b.EventID() != "" {
		rec.ID = b.EventID()
	}
	line, _ := json.Marshal(rec)
	if len(line) > MaxLine {
		return fmt.Errorf("record too large (%d bytes)", len(line))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if s.size()+int64(len(line))+1 > MaxBytes {
		s.addDropped(1)
		return ErrFull
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, "active.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func (s *Spool) size() int64 {
	var total int64
	entries, _ := os.ReadDir(s.Dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}

func (s *Spool) droppedPath() string { return filepath.Join(s.Dir, "dropped") }

func (s *Spool) addDropped(n int) {
	cur := s.Dropped()
	_ = os.WriteFile(s.droppedPath(), []byte(strconv.Itoa(cur+n)), 0o600)
}

// Dropped returns how many records were dropped since the last reset.
func (s *Spool) Dropped() int {
	b, err := os.ReadFile(s.droppedPath())
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// ResetDropped zeroes the counter after it was reported.
func (s *Spool) ResetDropped(reported int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.Dropped()
	left := cur - reported
	if left <= 0 {
		_ = os.Remove(s.droppedPath())
		return
	}
	_ = os.WriteFile(s.droppedPath(), []byte(strconv.Itoa(left)), 0o600)
}

// Pending counts queued records (approximate: counts lines).
func (s *Spool) Pending() int {
	n := 0
	for _, f := range s.files(true) {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64<<10), MaxLine+1024)
		for sc.Scan() {
			if len(sc.Bytes()) > 0 {
				n++
			}
		}
		fh.Close()
	}
	return n
}

func (s *Spool) files(includeActive bool) []string {
	entries, _ := os.ReadDir(s.Dir)
	var segs []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "seg-") && strings.HasSuffix(n, ".jsonl") {
			segs = append(segs, filepath.Join(s.Dir, n))
		}
	}
	sort.Strings(segs)
	if includeActive {
		if _, err := os.Stat(filepath.Join(s.Dir, "active.jsonl")); err == nil {
			segs = append(segs, filepath.Join(s.Dir, "active.jsonl"))
		}
	}
	return segs
}

// SendResult classifies one send attempt.
type SendResult int

const (
	Sent      SendResult = iota
	Retry                // network / 5xx / 429: stop and try later
	Permanent            // refused: move to dead/
)

// Lock takes the single-flusher lock. A lock older than lockTTL is stale.
func (s *Spool) Lock() (func(), bool) {
	_ = os.MkdirAll(s.Dir, 0o700)
	p := filepath.Join(s.Dir, ".lock")
	for i := 0; i < 2; i++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d", os.Getpid())
			f.Close()
			return func() { _ = os.Remove(p) }, true
		}
		info, serr := os.Stat(p)
		if serr == nil && time.Since(info.ModTime()) > lockTTL {
			_ = os.Remove(p)
			continue
		}
		return func() {}, false
	}
	return func() {}, false
}

// Locked reports whether a flusher holds a fresh lock.
func (s *Spool) Locked() bool {
	info, err := os.Stat(filepath.Join(s.Dir, ".lock"))
	return err == nil && time.Since(info.ModTime()) <= lockTTL
}

// Flush sends queued records oldest first with send. It holds the lock for
// the whole pass, rotates active.jsonl into a segment first, and on a Retry
// keeps every unsent line for the next pass. Returns counts.
func (s *Spool) Flush(ctx context.Context, send func(Record) SendResult) (sent, dead int, err error) {
	unlock, ok := s.Lock()
	if !ok {
		return 0, 0, nil
	}
	defer unlock()
	active := filepath.Join(s.Dir, "active.jsonl")
	if info, err := os.Stat(active); err == nil && info.Size() > 0 {
		seg := filepath.Join(s.Dir, fmt.Sprintf("seg-%020d.jsonl", time.Now().UnixNano()))
		_ = os.Rename(active, seg) // on Windows an open writer can make this fail; next pass retries
	}
	seen := map[string]bool{}
	for _, f := range s.files(false) {
		if ctx.Err() != nil {
			return sent, dead, ctx.Err()
		}
		lines, rerr := readLines(f)
		if rerr != nil {
			continue
		}
		var keep [][]byte
		stop := false
		for i, ln := range lines {
			var r Record
			if json.Unmarshal(ln, &r) != nil {
				dead++
				s.toDead(ln)
				continue
			}
			if seen[r.ID] { // duplicate line in the spool: send once
				continue
			}
			switch send(r) {
			case Sent:
				seen[r.ID] = true
				sent++
			case Permanent:
				dead++
				s.toDead(ln)
			case Retry:
				keep = append(keep, lines[i:]...)
				stop = true
			}
			if stop {
				break
			}
			_ = os.Chtimes(filepath.Join(s.Dir, ".lock"), time.Now(), time.Now())
		}
		if len(keep) == 0 {
			_ = os.Remove(f)
		} else {
			_ = writeLines(f, keep)
		}
		if stop {
			return sent, dead, nil
		}
	}
	return sent, dead, nil
}

func (s *Spool) toDead(line []byte) {
	d := filepath.Join(s.Dir, "dead")
	_ = os.MkdirAll(d, 0o700)
	f, err := os.OpenFile(filepath.Join(d, "dead.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	// Keep dead/ bounded too.
	if info, err := f.Stat(); err == nil && info.Size() > MaxBytes/4 {
		return
	}
	_, _ = f.Write(append(append([]byte{}, line...), '\n'))
}

func readLines(path string) ([][]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			out = append(out, []byte(ln))
		}
	}
	return out, nil
}

func writeLines(path string, lines [][]byte) error {
	tmp := path + ".tmp"
	var b strings.Builder
	for _, l := range lines {
		b.Write(l)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
