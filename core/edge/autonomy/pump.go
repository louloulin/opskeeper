package autonomy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Sender delivers spooled rows to the control plane.
//
// It is an interface because the tunnel is not this package's business: the
// pump's job is "when the link is back, hand the rows over, slowly", and the
// link's job is how to get them there. A node that has been offline for a
// week has an interesting story to tell, and a node that tells it in one
// request is a node the center cannot tell apart from an attack.
//
// The rate limit below is not politeness. Replaying a week of decisions in a
// single burst lands on the same endpoint as every other node's reconnect at
// the same moment, and the audit chain that is supposed to record what the
// fleet did during the outage is exactly the thing that goes down under it.
const (
	// DefaultReplayBatch is how many rows go up per drain.
	DefaultReplayBatch = 100
	// DefaultReplayInterval is how often a drain may happen.
	DefaultReplayInterval = 5 * time.Second
)

// Sender is the control-plane side of a replay.
type Sender interface {
	// Send delivers rows in order. A partial failure is a failure: the
	// chain is ordered, and a hole in it is worse than a delay.
	Send(ctx context.Context, rows []Row) error
}

// SenderFunc adapts a function to Sender.
type SenderFunc func(ctx context.Context, rows []Row) error

// Send implements Sender.
func (f SenderFunc) Send(ctx context.Context, rows []Row) error { return f(ctx, rows) }

// PumpOptions configures a Pump.
type PumpOptions struct {
	// Spool is the local record to drain. Required.
	Spool *Spool
	// Sender is where the rows go. Required.
	Sender Sender
	// Link decides when a drain may happen. Required: a pump that
	// replays into a closed tunnel is a pump that loses rows.
	Link Link
	// Batch bounds one drain. Default DefaultReplayBatch.
	Batch int
	// Interval bounds how often a drain may happen. Default
	// DefaultReplayInterval.
	Interval time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
	// Log is optional.
	Log *slog.Logger
}

// Pump replays a spool once the control plane is reachable again.
type Pump struct {
	spool   *Spool
	sender  Sender
	link    Link
	batch   int
	every   time.Duration
	now     func() time.Time
	log     *slog.Logger
	lastRun time.Time
}

// NewPump returns a Pump.
func NewPump(opts PumpOptions) (*Pump, error) {
	switch {
	case opts.Spool == nil:
		return nil, &ConfigError{Field: "Spool", Reason: "is required: a pump with nothing to drain is a pump that hides its own misconfiguration"}
	case opts.Sender == nil:
		return nil, &ConfigError{Field: "Sender", Reason: "is required: the rows have to go somewhere, and silently dropping them is not somewhere"}
	case opts.Link == nil:
		return nil, &ConfigError{Field: "Link", Reason: "is required: replaying into a closed tunnel loses rows"}
	}
	p := &Pump{
		spool:  opts.Spool,
		sender: opts.Sender,
		link:   opts.Link,
		batch:  opts.Batch,
		every:  opts.Interval,
		now:    opts.Now,
		log:    opts.Log,
	}
	if p.batch <= 0 {
		p.batch = DefaultReplayBatch
	}
	if p.every <= 0 {
		p.every = DefaultReplayInterval
	}
	if p.now == nil {
		p.now = time.Now
	}
	return p, nil
}

// DrainOnce sends at most one batch, and only when the link is up and the
// interval has elapsed.
//
// It returns how many rows the center now has. A drain that is skipped —
// link down, interval not elapsed, nothing spooled — returns zero and is
// not an error, because the common case is a node that is simply not
// supposed to be talking to anyone.
func (p *Pump) DrainOnce(ctx context.Context) (int, error) {
	if !p.link.Reach().Online {
		return 0, nil
	}
	now := p.now()
	if !p.lastRun.IsZero() && now.Sub(p.lastRun) < p.every {
		return 0, nil
	}

	rows, err := p.spool.Peek(p.batch)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := p.sender.Send(ctx, rows); err != nil {
		// Nothing is acked. The rows stay, and the next drain tries them
		// again in the same order — a partially delivered chain is not a
		// chain, so the ack is all-or-nothing.
		p.logf("autonomy replay failed", "rows", len(rows), "error", err)
		return 0, fmt.Errorf("autonomy: replay %d rows: %w", len(rows), err)
	}
	if err := p.spool.Ack(len(rows)); err != nil {
		return 0, err
	}
	p.lastRun = now
	return len(rows), nil
}

// Run drains on a ticker until the context ends.
//
// The first drain is immediate rather than after one interval: a node that
// has just come back from an outage has rows the operator is waiting to
// read, and making them wait five seconds for no reason is the difference
// between a story and a mystery.
func (p *Pump) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.every)
	defer ticker.Stop()
	for {
		if _, err := p.DrainOnce(ctx); err != nil && ctx.Err() == nil {
			// A failed drain is logged and retried; it is not fatal,
			// because the only thing that ends this loop is the node
			// stopping or the link going away again.
			if ctx.Err() == nil && !errors.Is(err, context.Canceled) {
				p.logf("autonomy replay will be retried", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Pending reports how many rows are waiting to go.
func (p *Pump) Pending() (int, error) { return p.spool.Len() }

func (p *Pump) logf(msg string, args ...any) {
	if p.log != nil {
		p.log.Error(msg, args...)
	}
}
