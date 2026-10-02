package main

// Closed-loop remediation adapter wiring.
//
// The approved phase of the closed loop dispatches a RemediationOption by
// looking its Action up in a tool registry and calling it. Until now the
// registry was assembled from exactly one adapter (PostgreSQL, behind
// OPSKEEPER_LOOP_PG_DSN), so every other action the investigator can write
// into a contract — k8s.rollout_undo, mq.drain_queue, host.restart_service —
// resolved to a name with nothing behind it. The gate that reports this
// (cmd/opskeeper-eval vocabulary, "Loop action executability") went green on
// names once the adapters existed; this file is what makes the names true in
// a running deployment rather than only in the report.
//
// Wiring is opt-in per adapter, and each one is independent: a deployment
// that only points at PostgreSQL behaves exactly as before, and one that
// adds a Kubernetes DSN gets those tools too without a rebuild. A DSN that
// fails to connect is logged and skipped rather than fatal, because the
// alternative — refusing to boot because one of five adapters is unreachable
// — would take the control plane down over a remediation convenience.

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/manager/knowledge/gitartifact"
	middlewareadapter "github.com/vincent-wuhan/opskeeper/core/manager/middleware/adapter"
	gitadapter "github.com/vincent-wuhan/opskeeper/core/manager/middleware/adapter/git"
	hostadapter "github.com/vincent-wuhan/opskeeper/core/manager/middleware/adapter/host"
	k8sadapter "github.com/vincent-wuhan/opskeeper/core/manager/middleware/adapter/k8s"
	mqadapter "github.com/vincent-wuhan/opskeeper/core/manager/middleware/adapter/mq"
	kafkaadapter "github.com/vincent-wuhan/opskeeper/core/manager/middleware/adapter/mq/kafka"
	rabbitmqadapter "github.com/vincent-wuhan/opskeeper/core/manager/middleware/adapter/mq/rabbitmq"
	pgadapter "github.com/vincent-wuhan/opskeeper/core/manager/middleware/adapter/postgres"
	redisadapter "github.com/vincent-wuhan/opskeeper/core/manager/middleware/adapter/redis"
	middlewareregistry "github.com/vincent-wuhan/opskeeper/core/manager/middleware/registry"
)

// loopAdapterConnectTimeout bounds a single adapter's connect. It is short
// because this runs during boot: an adapter that cannot connect in this
// window is one the process should start without, not one worth blocking on.
const loopAdapterConnectTimeout = 30 * time.Second

// loopAdapterSource is one optional adapter: where its DSN comes from, and
// how to connect it and register its tools.
type loopAdapterSource struct {
	name string
	env  string
	wire func(ctx context.Context, dsn string, reg *middlewareregistry.Registry) (closeFn func(), err error)
}

// loopAdapterSources is every adapter the approved phase can dispatch
// through, in the order they are reported. The environment variable names
// are part of the deployment contract and deliberately follow the existing
// OPSKEEPER_LOOP_PG_DSN rather than a new scheme.
func loopAdapterSources() []loopAdapterSource {
	return []loopAdapterSource{
		{
			name: "postgres",
			env:  "OPSKEEPER_LOOP_PG_DSN",
			wire: func(ctx context.Context, dsn string, reg *middlewareregistry.Registry) (func(), error) {
				a := pgadapter.New()
				if err := a.Connect(ctx, middlewareadapter.ConnectionSpec{DSN: dsn, Timeout: loopAdapterConnectTimeout}); err != nil {
					return nil, err
				}
				if err := pgadapter.RegisterTools(reg, a); err != nil {
					_ = a.Close(context.Background())
					return nil, err
				}
				return func() { _ = a.Close(context.Background()) }, nil
			},
		},
		{
			name: "redis",
			env:  "OPSKEEPER_LOOP_REDIS_DSN",
			wire: func(ctx context.Context, dsn string, reg *middlewareregistry.Registry) (func(), error) {
				a := redisadapter.New()
				if err := a.Connect(ctx, middlewareadapter.ConnectionSpec{DSN: dsn, Timeout: loopAdapterConnectTimeout}); err != nil {
					return nil, err
				}
				if err := redisadapter.RegisterTools(reg, a); err != nil {
					_ = a.Close(context.Background())
					return nil, err
				}
				return func() { _ = a.Close(context.Background()) }, nil
			},
		},
		{
			name: "k8s",
			env:  "OPSKEEPER_LOOP_K8S_DSN",
			wire: func(ctx context.Context, dsn string, reg *middlewareregistry.Registry) (func(), error) {
				a := k8sadapter.New()
				if err := a.Connect(ctx, middlewareadapter.ConnectionSpec{DSN: dsn, Timeout: loopAdapterConnectTimeout}); err != nil {
					return nil, err
				}
				if err := k8sadapter.RegisterTools(reg, a); err != nil {
					_ = a.Close(context.Background())
					return nil, err
				}
				return func() { _ = a.Close(context.Background()) }, nil
			},
		},
		{
			name: "mq",
			env:  "OPSKEEPER_LOOP_MQ_DSN",
			wire: func(ctx context.Context, dsn string, reg *middlewareregistry.Registry) (func(), error) {
				a := mqadapter.New()
				if err := a.Connect(ctx, middlewareadapter.ConnectionSpec{DSN: dsn, Timeout: loopAdapterConnectTimeout}); err != nil {
					return nil, err
				}
				if err := mqadapter.RegisterTools(reg, a); err != nil {
					_ = a.Close(context.Background())
					return nil, err
				}
				return func() { _ = a.Close(context.Background()) }, nil
			},
		},
		{
			// The product-namespaced MQ tools. Until these two entries
			// existed, `kafka.*` and `rabbitmq.*` were registered by the
			// capability gate and by nothing else: cmd/opskeeper-eval
			// counted them as capabilities the platform has, and a running
			// control plane had never heard of them. That is the worst
			// possible state for a gate — it measures a fleet that does
			// not exist, so every number it reports is about a deployment
			// nobody can build. Both are opt-in and both are independent
			// of OPSKEEPER_LOOP_MQ_DSN: a deployment can serve the
			// neutral names, the product names, or both, and each
			// connection is separate so a wrong DSN in one does not take
			// the others down.
			name: "kafka",
			env:  "OPSKEEPER_LOOP_KAFKA_DSN",
			wire: func(ctx context.Context, dsn string, reg *middlewareregistry.Registry) (func(), error) {
				a := kafkaadapter.New()
				if err := a.Connect(ctx, middlewareadapter.ConnectionSpec{DSN: dsn, Timeout: loopAdapterConnectTimeout}); err != nil {
					return nil, err
				}
				if err := kafkaadapter.RegisterTools(reg, a); err != nil {
					_ = a.Close(context.Background())
					return nil, err
				}
				return func() { _ = a.Close(context.Background()) }, nil
			},
		},
		{
			name: "rabbitmq",
			env:  "OPSKEEPER_LOOP_RABBITMQ_DSN",
			wire: func(ctx context.Context, dsn string, reg *middlewareregistry.Registry) (func(), error) {
				a := rabbitmqadapter.New()
				if err := a.Connect(ctx, middlewareadapter.ConnectionSpec{DSN: dsn, Timeout: loopAdapterConnectTimeout}); err != nil {
					return nil, err
				}
				if err := rabbitmqadapter.RegisterTools(reg, a); err != nil {
					_ = a.Close(context.Background())
					return nil, err
				}
				return func() { _ = a.Close(context.Background()) }, nil
			},
		},
		{
			// git is not in the closed loop's remediation vocabulary
			// (see investigatorreal.RemediationActions), so wiring it
			// changes no loop number. It is wired anyway because the
			// git.* tools are what let an investigator check a claim
			// against the repository — "was this RPC added in the
			// deploy we are looking at" is answered from git, not from
			// a dashboard — and a tool that is only reachable when a
			// separate process happens to register it is a tool that
			// silently is not there.
			name: "git",
			env:  "OPSKEEPER_LOOP_GIT_DSN",
			wire: func(ctx context.Context, dsn string, reg *middlewareregistry.Registry) (func(), error) {
				// The LinkerRegistry is empty here: find_runtime_link's
				// reverse index is populated by the git-artifact
				// indexer at runtime, and a lookup with no linker
				// registered is a plain miss rather than an error.
				a := gitadapter.New(gitartifact.NewLinkerRegistry())
				if err := a.Connect(ctx, middlewareadapter.ConnectionSpec{DSN: dsn, Timeout: loopAdapterConnectTimeout}); err != nil {
					return nil, err
				}
				if err := gitadapter.RegisterTools(reg, a); err != nil {
					_ = a.Close(context.Background())
					return nil, err
				}
				return func() { _ = a.Close(context.Background()) }, nil
			},
		},
		{
			name: "host",
			env:  "OPSKEEPER_LOOP_HOST_DSN",
			wire: func(ctx context.Context, dsn string, reg *middlewareregistry.Registry) (func(), error) {
				a := hostadapter.New()
				if err := a.Connect(ctx, middlewareadapter.ConnectionSpec{DSN: dsn, Timeout: loopAdapterConnectTimeout}); err != nil {
					return nil, err
				}
				if err := hostadapter.RegisterTools(reg, a); err != nil {
					_ = a.Close(context.Background())
					return nil, err
				}
				return func() { _ = a.Close(context.Background()) }, nil
			},
		},
	}
}

// wireLoopRemediationAdapters connects every adapter with a configured DSN,
// registers its tools into reg, and returns the closers to run at shutdown.
//
// The returned slice is empty when nothing is configured, which is the same
// observable state as before this existed: an empty registry and an
// invoker that refuses with a reason instead of advancing the run.
func wireLoopRemediationAdapters(ctx context.Context, log *slog.Logger, reg *middlewareregistry.Registry) []func() {
	var closers []func()
	for _, src := range loopAdapterSources() {
		dsn := strings.TrimSpace(os.Getenv(src.env))
		if dsn == "" {
			continue
		}
		connectCtx, cancel := context.WithTimeout(ctx, loopAdapterConnectTimeout)
		closeFn, err := src.wire(connectCtx, dsn, reg)
		cancel()
		if err != nil {
			// The env var names the adapter in full rather than a
			// credential, so quoting it back is safe and is the difference
			// between "something failed" and "this deployment's k8s DSN is
			// wrong".
			log.Error("loop: remediation adapter failed to connect; its tools will not be dispatchable",
				slog.String("adapter", src.name), slog.String("env", src.env), slog.Any("err", err))
			continue
		}
		closers = append(closers, closeFn)
		log.Info("loop: remediation adapter wired", slog.String("adapter", src.name))
	}
	return closers
}
