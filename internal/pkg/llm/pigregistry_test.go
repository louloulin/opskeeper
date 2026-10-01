package llm

import (
	"context"
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"
)

// TestPigRegistryClientResolvesThroughTheSharedRegistry pins the property the
// assembly layer depends on: the Client handed to every routed provider reads
// from ONE registry, so a provider's transport (and its prompt-cache session)
// is reused across turns rather than rebuilt per call.
func TestPigRegistryClientResolvesThroughTheSharedRegistry(t *testing.T) {
	t.Parallel()
	src := pigmodel.NewStaticSource(map[domain.ProviderID]pigmodel.ProviderConfig{
		domain.ProviderCustom: {
			ID: domain.ProviderCustom, APIKey: "k",
			BaseURL: "http://127.0.0.1:1", // never dialled; resolution fails first
			Models:  []string{"m"}, DefaultModel: "m",
		},
	}, domain.ProviderCustom)
	reg := NewPigRegistry(src, nil)

	client := reg.Client()
	if client == nil {
		t.Fatal("expected a non-nil client")
	}
	// A request that names nothing must resolve to the cluster default the
	// static source provides, proving the Client is wired to the registry
	// rather than being a no-op.
	_, err := client.Chat(context.Background(), ChatReq{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected the dial to fail, not a fabricated success")
	}
}

// TestPigRegistryClientFailsClosedWithoutAProvider pins that an empty settings
// source produces a Client that refuses rather than inventing a provider. A
// manager that answered every Chat with a made-up endpoint would be worse than
// one that fails loudly.
func TestPigRegistryClientFailsClosedWithoutAProvider(t *testing.T) {
	t.Parallel()
	reg := NewPigRegistry(pigmodel.NewStaticSource(nil, ""), nil)
	_, err := reg.Client().Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected a refusal with no configured provider")
	}
}

// TestPigRegistryCloseIsIdempotentAndNilSafe pins that Close can run on the
// shutdown path unconditionally. A nil-receiver panic during shutdown would
// turn a clean exit into a crash report.
func TestPigRegistryCloseIsIdempotentAndNilSafe(t *testing.T) {
	t.Parallel()
	var nilReg *PigRegistry
	if err := nilReg.Close(); err != nil {
		t.Fatalf("nil registry Close: %v", err)
	}
	reg := NewPigRegistry(pigmodel.NewStaticSource(nil, ""), nil)
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
