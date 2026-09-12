package pirpc

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
)

// recordHead is the minimum every stdout record shares. Decoding it
// first keeps an unknown record type from failing the whole session.
type recordHead struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Method string `json:"method"`
}

// readLoop consumes Pi's stdout until EOF or a fatal framing error,
// routing responses to their waiter and everything else to OnEvent.
func (s *Session) readLoop(stdout io.Reader) {
	lr := newLineReader(stdout, s.maxLine)
	var termErr error
	for {
		line, err := lr.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				termErr = ErrSessionClosed
			} else {
				termErr = err
			}
			break
		}
		if len(line) == 0 {
			continue
		}
		var head recordHead
		if err := json.Unmarshal(line, &head); err != nil {
			// A non-JSON line means something other than the RPC
			// peer wrote to stdout (a node warning, a wrapper
			// script's echo). Log and skip: dropping the session
			// over foreign output would make the edge brittle.
			s.log.Warn("pirpc: skipping non-JSON record on stdout",
				slog.Int("bytes", len(line)))
			continue
		}

		switch {
		case head.Type == "response" && head.ID != "":
			s.deliver(head.ID, line)
		case head.Type == "extension_ui_request":
			s.answerUI(head)
			s.emit(head.Type, line)
		default:
			// Responses without an id cannot be correlated (Pi emits
			// these for parse failures). They are surfaced as events
			// so nothing is dropped silently.
			s.emit(head.Type, line)
		}
	}

	s.mu.Lock()
	s.readErr = termErr
	waiters := s.waiters
	s.waiters = make(map[string]chan *Response)
	s.mu.Unlock()

	for _, ch := range waiters {
		close(ch)
	}
	close(s.doneCh)
}

func (s *Session) deliver(id string, line []byte) {
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		s.log.Warn("pirpc: undecodable response", slog.String("id", id), slog.Any("err", err))
		return
	}
	s.mu.Lock()
	ch, ok := s.waiters[id]
	if ok {
		delete(s.waiters, id)
	}
	s.mu.Unlock()
	if !ok {
		// Late response for a cancelled command. Expected after a
		// context deadline; not an error.
		s.log.Debug("pirpc: response with no waiter", slog.String("id", id))
		return
	}
	ch <- &resp
	close(ch)
}

func (s *Session) emit(typ string, line []byte) {
	if s.onEvent == nil {
		return
	}
	raw := make([]byte, len(line))
	copy(raw, line)
	s.onEvent(Event{Type: typ, Raw: raw})
}

// answerUI replies to a blocking extension dialog. Fail closed: a
// dialog is cancelled and a confirm is denied. Fire-and-forget
// methods (notify, setStatus, setWidget, setTitle, set_editor_text)
// expect no reply and get none.
func (s *Session) answerUI(head recordHead) {
	if s.ui == UIIgnore || head.ID == "" {
		return
	}
	switch head.Method {
	case "select", "input", "editor":
		if err := s.Send(map[string]any{
			"type":      "extension_ui_response",
			"id":        head.ID,
			"cancelled": true,
		}); err != nil {
			s.log.Warn("pirpc: cannot cancel extension dialog",
				slog.String("method", head.Method), slog.Any("err", err))
		}
	case "confirm":
		if err := s.Send(map[string]any{
			"type":      "extension_ui_response",
			"id":        head.ID,
			"confirmed": false,
		}); err != nil {
			s.log.Warn("pirpc: cannot deny extension confirm", slog.Any("err", err))
		}
	default:
		// Fire-and-forget or unknown method: no reply expected.
	}
}
