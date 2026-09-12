package pirpc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// DefaultMaxLineBytes bounds a single JSONL record. Pi's own
// `get_commands` response grows with the number of installed skills
// and extensions and passes 64 KiB on an ordinary host, so a small
// limit is a correctness bug, not a hardening measure. 8 MiB is
// generous enough for a full message history dump while still
// bounding memory if the peer goes haywire.
const DefaultMaxLineBytes = 8 << 20

// ErrLineTooLong is returned when a record exceeds the configured
// limit. It is deliberately fatal for the session: a truncated
// record cannot be parsed, and silently dropping it would desync
// request/response correlation.
var ErrLineTooLong = errors.New("pirpc: record exceeds max line bytes")

// lineReader reads LF-delimited records off Pi's stdout.
//
// It exists instead of bufio.Scanner for two reasons: Scanner caps a
// token at a preallocated buffer size and reports a non-specific
// error past it, and any split function that understands Unicode
// line separators would break records containing U+2028 / U+2029
// inside JSON strings. This reader splits on the byte 0x0A only.
type lineReader struct {
	br  *bufio.Reader
	max int
}

func newLineReader(r io.Reader, max int) *lineReader {
	if max <= 0 {
		max = DefaultMaxLineBytes
	}
	return &lineReader{br: bufio.NewReaderSize(r, 64<<10), max: max}
}

// readLine returns the next record without its trailing LF, with one
// optional trailing CR stripped. A final record without a trailing
// LF is returned before io.EOF surfaces on the following call.
func (lr *lineReader) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := lr.br.ReadSlice('\n')
		if len(buf)+len(chunk) > lr.max {
			return nil, fmt.Errorf("%w (%d bytes)", ErrLineTooLong, len(buf)+len(chunk))
		}
		if err == nil {
			if buf == nil {
				// Fast path: the whole record was already in the
				// bufio buffer. Copy because ReadSlice hands back
				// buffer-backed memory that the next read reuses.
				out := make([]byte, len(chunk))
				copy(out, chunk)
				return trimRecord(out), nil
			}
			buf = append(buf, chunk...)
			return trimRecord(buf), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			buf = append(buf, chunk...)
			continue
		}
		// Real error or EOF. Surface any bytes read so far as the
		// final record; the caller sees the error on the next call.
		if len(chunk) > 0 || len(buf) > 0 {
			buf = append(buf, chunk...)
			if len(trimRecord(buf)) == 0 {
				return nil, err
			}
			return trimRecord(buf), nil
		}
		return nil, err
	}
}

// trimRecord drops the delimiter and a single preceding CR so a peer
// that emits CRLF (a Windows shim, a proxy) still parses.
func trimRecord(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}
