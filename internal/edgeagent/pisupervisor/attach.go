package pisupervisor

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/pirpc"
)

// SessionSink receives the RPC session for each spawned child so the
// rest of the edge (RCA driver, audit uplink) can talk to the live
// Pi. It is called before the first probe and must not block.
//
// A restart produces a brand-new session; the previous one is closed
// and must not be reused. Implementations therefore store the
// session behind a lock and swap it wholesale.
type SessionSink interface {
	SetPiSession(*pirpc.Session)
	ClearPiSession()
}

// RPCAttach returns a Config.Attach that speaks Pi's real RPC
// protocol over the child's stdio.
//
// The liveness probe is a get_state round-trip. That is the honest
// health signal for Pi: it proves the child is reading stdin,
// framing JSONL and answering. A live PID proves none of that, and
// the HTTP /health endpoint assumed by earlier revisions of
// plan1.0.md does not exist in any published Pi release.
//
// sink may be nil when the caller only wants supervision. onEvent
// may be nil to drop Pi's event stream; it is invoked from the
// session's reader goroutine and must not block.
func RPCAttach(sink SessionSink, onEvent func(pirpc.Event), closeGrace time.Duration) func(context.Context, io.WriteCloser, io.Reader) (func(context.Context) error, func(), error) {
	if closeGrace <= 0 {
		closeGrace = 5 * time.Second
	}
	return func(ctx context.Context, stdin io.WriteCloser, stdout io.Reader) (func(context.Context) error, func(), error) {
		session, err := pirpc.NewSession(pirpc.SessionOptions{
			Stdin:   stdin,
			Stdout:  stdout,
			OnEvent: onEvent,
			// UIDeny All is the zero value: an extension dialog is
			// never an approval channel on an unattended edge.
		})
		if err != nil {
			return nil, nil, err
		}
		if sink != nil {
			sink.SetPiSession(session)
		}

		probe := func(pctx context.Context) error {
			state, err := session.GetState(pctx)
			if err != nil {
				return fmt.Errorf("pisupervisor: pi rpc probe: %w", err)
			}
			// A Pi with no usable provider still answers get_state.
			// That is degraded, not dead: restarting it would not
			// conjure credentials, so the supervisor keeps it and
			// lets the cloud see the degraded state instead.
			_ = state
			return nil
		}

		teardown := func() {
			if sink != nil {
				sink.ClearPiSession()
			}
			_ = session.Close()
			_ = stdin.Close()
		}
		return probe, teardown, nil
	}
}
