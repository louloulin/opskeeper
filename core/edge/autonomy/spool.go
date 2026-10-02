package autonomy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Spool is the node's local record of the decisions it made on its own.
//
// The plan's line is "每次自治执行写本地审计 spool；隧道恢复后回传，补写中心审计链",
// and the interesting half is the failure modes rather than the mechanism.
// Three of them are the reason this is a file on the node and not a channel
// to the center:
//
//   - The row must exist **before** the action runs. A tunnel call that
//     fails is exactly the case the spool exists for, so a sink that only
//     receives rows on the success path records nothing at all.
//   - The row must survive a power cut. So the format is one JSON object
//     per line, a half-written line is discarded on read rather than
//     failing the replay, and the file is opened 0600 — a node's autonomy
//     decisions are a list of things it did to itself without asking, and
//     a world-readable one of those is an incident report for someone else
//     to read.
//   - The file must not grow without bound. A node that is off the network
//     for a week should not fill its own disk with the evidence of that
//     week, and the evidence is least useful oldest: once the newest rows
//     are gone, the outage is over and nobody is reading the log.
//
// It is deliberately not a general-purpose queue. There is one writer (the
// arbiter, under the gate) and one reader (the replay pump, on reconnect),
// and a design that allowed more would be a design with a locking protocol
// nobody had a reason to get right.

const (
	// DefaultSpoolBytes is how large the spool may grow before the oldest
	// rows are dropped. It is sized for a week of offline self-healing at
	// a rate no sane node reaches, so in practice the cap is a guard
	// against a run loop rather than a policy.
	DefaultSpoolBytes = 8 << 20
	// spoolDirMode is the mode a spool directory is created with.
	spoolDirMode = 0o700
	// spoolFileMode is the mode the spool itself is created with: owner
	// read-write only.
	spoolFileMode = 0o600
	// keepFloor is how many of the newest rows a compaction always keeps.
	//
	// A node that has been alone long enough to fill the spool is the node
	// whose most recent decisions matter most, and a compaction that kept
	// nothing would delete the evidence of the outage in progress in order
	// to save space. A hundred rows is a few tens of kilobytes and is
	// enough to show what the node was doing when the disk filled.
	keepFloor = 100
)

// Spool is a bounded, owner-only, line-oriented audit file.
type Spool struct {
	path     string
	maxBytes int64

	mu sync.Mutex
	f  *os.File
	// size is the file's length as this process last knew it. It is
	// tracked rather than stat'd on every append because the compaction
	// check runs on the path of a self-heal, which is a path where a
	// syscall nobody asked for is a cost nobody chose.
	size int64
}

// OpenSpool opens or creates the spool at path.
//
// The file is created 0600 and the directory 0700, and an existing file
// that is group- or world-readable is refused rather than quietly widened:
// a spool that was already too open is evidence that something else on the
// host is too open, and the arbiter's first act should not be to paper
// over it.
//
// A symlink at the path is refused for the same reason. The path comes from
// configuration, and a node writing audit rows through a link somebody else
// planted is a node whose audit trail ends up somewhere its owner did not
// choose.
func OpenSpool(path string, maxBytes int64) (*Spool, error) {
	if path == "" {
		return nil, errors.New("autonomy: the audit spool needs a path")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultSpoolBytes
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("autonomy: the audit spool at %s is a symlink, and this host will not write audit rows through one", path)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return nil, fmt.Errorf("autonomy: the audit spool at %s is mode %04o; owner-only is required, and this host will not widen a file it did not create", path, perm)
		}
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, spoolDirMode); err != nil {
			return nil, fmt.Errorf("autonomy: create the audit spool directory: %w", err)
		}
	}
	// O_RDWR because a replay has to read the file this same handle owns,
	// and opening it write-only would turn every replay into EBADF. The
	// O_APPEND is what makes a row land whole even if a second writer ever
	// appears: the offset is chosen by the kernel, after the write, not by
	// whatever this process last read.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, spoolFileMode)
	if err != nil {
		return nil, fmt.Errorf("autonomy: open the audit spool: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("autonomy: stat the audit spool: %w", err)
	}
	return &Spool{path: path, maxBytes: maxBytes, f: f, size: info.Size()}, nil
}

// Path is where the spool lives, for a health page that wants to show it.
func (s *Spool) Path() string { return s.path }

// Record appends one row. It satisfies Audit.
//
// A full disk is not an error the caller has to handle: a node that has run
// out of room has bigger problems than a missing audit row, and refusing
// the self-heal because of it converts an availability problem into a
// correctness one. The row is still written when there is room for it, and
// the cap keeps "when there is room" true most of the time.
func (s *Spool) Record(_ context.Context, row Row) error {
	line, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("autonomy: encode an audit row: %w", err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return errors.New("autonomy: the audit spool is closed")
	}
	if _, err := s.f.Write(line); err != nil {
		return fmt.Errorf("autonomy: append an audit row: %w", err)
	}
	s.size += int64(len(line))
	if s.size > s.maxBytes {
		// Best effort, and deliberately so: a spool that cannot compact
		// is a spool that is full, and refusing the action would make a
		// disk problem look like a policy decision.
		_ = s.compactLocked(keepFloor)
	}
	return nil
}

// Replay hands every stored row to fn, oldest first, and returns how many
// were delivered.
//
// A line that does not parse is skipped rather than treated as an error: it
// is almost certainly the tail of a write that a power cut interrupted, and
// a spool that cannot be read because of its own last row is a spool that
// will never be replayed. The rows after it are still good.
func (s *Spool) Replay(fn func(Row) error) (int, error) {
	rows, err := s.read()
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, row := range rows {
		if err := fn(row); err != nil {
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}

// Peek returns up to n of the oldest rows without removing them.
//
// It is what the replay pump reads: a drain that delivered rows and then
// failed to ack them must be able to look again, and a pump that read the
// file by removing from it could not be retried.
func (s *Spool) Peek(n int) ([]Row, error) {
	rows, err := s.read()
	if err != nil {
		return nil, err
	}
	if n > 0 && n < len(rows) {
		return rows[:n:n], nil
	}
	return rows, nil
}

// Ack drops the first n rows, which is how the replay pump says the center
// has them.
//
// It rewrites the file rather than truncating in place, because truncating
// the head of a file this process is still appending to would leave a hole
// where a row used to be. The replacement is written beside the spool and
// renamed over it, so a crash mid-ack leaves the whole old file rather than
// half of one.
func (s *Spool) Ack(n int) error {
	if n <= 0 {
		return nil
	}
	rows, err := s.read()
	if err != nil {
		return err
	}
	if n > len(rows) {
		n = len(rows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rewriteLocked(rows[n:])
}

// Len reports how many rows the spool holds.
func (s *Spool) Len() (int, error) {
	rows, err := s.read()
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}

// Close releases the file.
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// read parses the whole spool.
func (s *Spool) read() ([]Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

// readLocked is read for a caller that already holds the lock. The split is
// not tidiness: Record compacts while holding the lock, so a read that took
// it again would deadlock the one path that runs during an outage.
func (s *Spool) readLocked() ([]Row, error) {
	rows, _, err := s.readSizedLocked()
	return rows, err
}

// compactLocked drops the oldest rows until the file is inside its cap.
//
// The budget is walked backwards from the newest row, accumulating the size
// each row already occupies on disk. The earlier version re-encoded the
// kept set on every iteration of a loop that dropped one row at a time,
// which is quadratic in the spool's length — and a spool that compacts is
// by definition a long one, reached on the path of a self-heal during an
// outage. Measuring what is already written is both cheaper and exact.
func (s *Spool) compactLocked(keep int) error {
	rows, sizes, err := s.readSizedLocked()
	if err != nil {
		return err
	}
	if len(rows) <= max(keep, keepFloor) {
		return nil
	}
	// Half the cap rather than all of it, so a spool sitting exactly at its
	// limit does not compact on every single append and turn a steady
	// stream of self-heals into a steady stream of full rewrites.
	budget := s.maxBytes / 2
	var used int64
	first := len(rows)
	for i := len(rows) - 1; i >= 0; i-- {
		if used+sizes[i] > budget {
			break
		}
		used += sizes[i]
		first = i
	}
	// Never below the floor: a node that has been alone long enough to fill
	// the spool is the node whose most recent decisions matter most, and a
	// compaction that kept nothing would delete the evidence of the outage
	// in progress in order to save space.
	first = min(first, len(rows)-keepFloor)
	first = max(first, 0)
	if first == 0 {
		return nil
	}
	return s.rewriteLocked(rows[first:])
}

// readSizedLocked parses the spool and reports each row's size on disk, so
// a compaction can budget without re-encoding anything.
func (s *Spool) readSizedLocked() ([]Row, []int64, error) {
	if s.f == nil {
		return nil, nil, errors.New("autonomy: the audit spool is closed")
	}
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("autonomy: rewind the audit spool: %w", err)
	}
	var rows []Row
	var sizes []int64
	sc := bufio.NewScanner(s.f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var row Row
		if err := json.Unmarshal(line, &row); err != nil {
			// An interrupted write, or a file edited by hand. Either way
			// the rows around it are still worth having.
			continue
		}
		rows = append(rows, row)
		sizes = append(sizes, int64(len(line))+1)
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("autonomy: read the audit spool: %w", err)
	}
	if _, err := s.f.Seek(0, io.SeekEnd); err != nil {
		return nil, nil, fmt.Errorf("autonomy: rewind the audit spool: %w", err)
	}
	return rows, sizes, nil
}

func (s *Spool) rewriteLocked(rows []Row) error {
	tmp := s.path + ".rewrite"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, spoolFileMode)
	if err != nil {
		return fmt.Errorf("autonomy: rewrite the audit spool: %w", err)
	}
	w := bufio.NewWriter(f)
	var size int64
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			f.Close()
			return fmt.Errorf("autonomy: encode an audit row: %w", err)
		}
		encoded = append(encoded, '\n')
		n, err := w.Write(encoded)
		if err != nil {
			f.Close()
			return fmt.Errorf("autonomy: rewrite the audit spool: %w", err)
		}
		size += int64(n)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return fmt.Errorf("autonomy: rewrite the audit spool: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("autonomy: sync the audit spool: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("autonomy: rewrite the audit spool: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("autonomy: replace the audit spool: %w", err)
	}
	// The descriptor still points at the unlinked original, so it is
	// reopened rather than reused: an append through it would write rows
	// nobody can read back.
	opened, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, spoolFileMode)
	if err != nil {
		return fmt.Errorf("autonomy: reopen the audit spool: %w", err)
	}
	if s.f != nil {
		_ = s.f.Close()
	}
	s.f = opened
	s.size = size
	return nil
}
