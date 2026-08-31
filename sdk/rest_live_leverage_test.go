package godark_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	godark "github.com/gq-godark/gdx-go-sdk"
)

func liveEdgeEnabled() bool {
	return os.Getenv("GDX_LIVE_EDGE") == "1"
}

// Live REST leverage integration test against a running gdx-edge.
//
// Gated by GDX_LIVE_EDGE=1. Run with:
//
//	GDX_LIVE_EDGE=1 GDX_REST_URL=http://127.0.0.1:4000 \
//	  GDX_API_KEY_ID=... GDX_API_SECRET=... \
//	  go test -run TestRestLiveLeverage -count=1 -v
func TestRestLiveLeverage(t *testing.T) {
	if !liveEdgeEnabled() {
		t.Skip("GDX_LIVE_EDGE!=1; skipping live leverage test")
	}

	base := os.Getenv("GDX_REST_URL")
	if base == "" {
		base = "http://127.0.0.1:4000"
	}
	symbol := os.Getenv("GDX_LIVE_SYMBOL")
	if symbol == "" {
		symbol = "BTC-USDC-PERP"
	}

	cfg := godark.RestClientConfig{BaseURL: base}
	if keyID := os.Getenv("GDX_API_KEY_ID"); keyID != "" {
		cfg.APIKeyID = keyID
		cfg.APISecret = os.Getenv("GDX_API_SECRET")
		cfg.Passphrase = os.Getenv("GDX_PASSPHRASE")
	} else if key := os.Getenv("GDX_API_KEY"); key != "" {
		cfg.APIKey = key
	} else {
		cfg.APIKey = "test-key-1"
	}

	client, err := godark.NewRestClient(cfg)
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	before, err := client.GetLeverage(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "405") {
			t.Skip("GET /api/v1/leverage not supported on this edge (405)")
		}
		t.Fatalf("GetLeverage (before): %v", err)
	}
	if before.Settings == nil {
		t.Fatalf("GetLeverage: settings slice is nil")
	}

	ack, err := client.UpdateLeverage(ctx, symbol, 5)
	if err != nil {
		t.Fatalf("UpdateLeverage(5): %v", err)
	}
	if !ack.Success {
		t.Fatalf("UpdateLeverage ack: %+v", ack)
	}

	symbolID, _ := godark.DefaultSymbolMap()[symbol]
	after, err := client.GetLeverage(ctx)
	if err != nil {
		t.Fatalf("GetLeverage (after): %v", err)
	}
	found := false
	for _, row := range after.Settings {
		if row.SymbolID == symbolID && row.Leverage == 5 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("GetLeverage after update: no entry for symbol_id=%d leverage=5 in %+v", symbolID, after.Settings)
	}

	resetAck, err := client.UpdateLeverage(ctx, symbol, 1)
	if err != nil {
		t.Fatalf("UpdateLeverage(1 reset): %v", err)
	}
	if !resetAck.Success {
		t.Fatalf("reset ack: %+v", resetAck)
	}
}
