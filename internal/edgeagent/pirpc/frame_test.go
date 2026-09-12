package pirpc

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLineSplitsOnLFOnly(t *testing.T) {
	// U+2028 and U+2029 are line separators to Unicode-aware readers
	// but are legal characters inside a JSON string. Pi's docs call
	// this out explicitly (Node's readline is not protocol-compliant
	// for that reason); splitting on them corrupts the record.
	in := "{\"type\":\"a\",\"text\":\"line still same record here\"}\n" +
		"{\"type\":\"b\"}\n"
	lr := newLineReader(strings.NewReader(in), 0)

	first, err := lr.readLine()
	if err != nil {
		t.Fatalf("first readLine: %v", err)
	}
	if !strings.Contains(string(first), "same record here") {
		t.Fatalf("record was split on a Unicode separator: %q", first)
	}
	second, err := lr.readLine()
	if err != nil {
		t.Fatalf("second readLine: %v", err)
	}
	if string(second) != `{"type":"b"}` {
		t.Fatalf("second record = %q", second)
	}
	if _, err := lr.readLine(); !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestReadLineStripsTrailingCR(t *testing.T) {
	lr := newLineReader(strings.NewReader("{\"type\":\"a\"}\r\n"), 0)
	got, err := lr.readLine()
	if err != nil {
		t.Fatalf("readLine: %v", err)
	}
	if string(got) != `{"type":"a"}` {
		t.Fatalf("CR not stripped: %q", got)
	}
}

func TestReadLineHandlesRecordsLargerThanBufio(t *testing.T) {
	// Real driver: a get_commands response on a host with a normal
	// set of skills installed is well past 64 KiB.
	big := strings.Repeat("x", 300<<10)
	lr := newLineReader(strings.NewReader(`{"type":"a","pad":"`+big+`"}`+"\n"), 0)
	got, err := lr.readLine()
	if err != nil {
		t.Fatalf("readLine: %v", err)
	}
	if len(got) < 300<<10 {
		t.Fatalf("record truncated: %d bytes", len(got))
	}
}

func TestReadLineRejectsOversizeRecord(t *testing.T) {
	lr := newLineReader(strings.NewReader(strings.Repeat("y", 5000)+"\n"), 1000)
	if _, err := lr.readLine(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("want ErrLineTooLong, got %v", err)
	}
}

func TestReadLineReturnsFinalRecordWithoutNewline(t *testing.T) {
	lr := newLineReader(strings.NewReader(`{"type":"a"}`), 0)
	got, err := lr.readLine()
	if err != nil {
		t.Fatalf("readLine: %v", err)
	}
	if string(got) != `{"type":"a"}` {
		t.Fatalf("got %q", got)
	}
	if _, err := lr.readLine(); !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}
}
