// GoDark Go SDK -- REST-only trader demo
//
// Mirrors the docs onboarding flow: auth -> session.setup -> encrypted
// place -> get_order snapshot -> await_terminal_status -> cancel.
//
// Run with:
//
//	GODARK_REST_URL=http://127.0.0.1:4000 \
//	GODARK_API_KEY_ID=... GODARK_API_SECRET=... \
//	  go run ./examples/full_trader_rest
//
// Falls back to legacy `test-key-1` static key when no key id/secret env
// vars are set (useful against legacy localnet edges).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk/examples/internal/envloader"
)

const symbol = "BTC-USDC-PERP"

func main() {
	envloader.LoadDotenv()

	base := os.Getenv("GODARK_REST_URL")
	if base == "" {
		base = os.Getenv("GDX_REST_URL")
	}
	if base == "" {
		base = "http://127.0.0.1:4000"
	}

	keyID := os.Getenv("GODARK_API_KEY_ID")
	if keyID == "" {
		keyID = os.Getenv("GDX_API_KEY_ID")
	}
	secret := os.Getenv("GODARK_API_SECRET")
	if secret == "" {
		secret = os.Getenv("GDX_API_SECRET")
	}

	cfg := godark.RestClientConfig{BaseURL: base}
	if keyID != "" && secret != "" {
		cfg.APIKeyID = keyID
		cfg.APISecret = secret
	} else {
		cfg.APIKey = "test-key-1"
	}

	client, err := godark.NewRestClient(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	fmt.Println("connecting (auth + ECDH session.setup)...")
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Disconnect(ctx) }()
	fmt.Printf("session established: %v\n", client.IsSessionEstablished())

	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRestRequest{
		PlaceOrderRequest: godark.PlaceOrderRequest{
			Symbol:    symbol,
			Side:      godark.SideBuy,
			OrderType: godark.OrderTypeLimit,
			Quantity:  0.001,
			Price:     30_000,
		},
		ClientOrderID: "sdk-go-rest-demo",
	})
	if err != nil {
		envloader.PrintOrderError("PlaceOrder", err)
		os.Exit(3)
	}
	fmt.Printf("placed: order_id=%s sequence=%s\n", ack.OrderID, ack.Sequence)

	if snap, err := client.GetOrder(ctx, ack.OrderID); err == nil {
		fmt.Printf("get_order snapshot: %v\n", snap)
	}

	_, _ = client.AwaitTerminalStatus(ctx, ack.OrderID, 2*time.Second, 250*time.Millisecond)

	if _, err := client.CancelOrder(ctx, ack.OrderID, symbol); err != nil {
		envloader.PrintOrderError("CancelOrder", err)
	}

	fmt.Println("done")
}
