// GoDark Go SDK -- Docs onboarding minimal snippet
//
// RFC 6749 auth + a single limit order via the REST trading client, using
// the docs field names. Mirrors the python `quickstart_docs.py` and rust
// `quickstart_docs.rs` examples.
//
// Run with:
//
//	GODARK_API_KEY_ID=... GODARK_API_SECRET=... go run ./examples/quickstart_docs
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk/examples/internal/envloader"
)

const symbol = "BTC-USDC-PERP"

func main() {
	envloader.LoadDotenv()

	keyID := os.Getenv("GODARK_API_KEY_ID")
	if keyID == "" {
		keyID = os.Getenv("GDX_API_KEY_ID")
	}
	secret := os.Getenv("GODARK_API_SECRET")
	if secret == "" {
		secret = os.Getenv("GDX_API_SECRET")
	}
	if keyID == "" || secret == "" {
		log.Fatal("GODARK_API_KEY_ID and GODARK_API_SECRET are required")
	}

	base := os.Getenv("GODARK_REST_URL")
	if base == "" {
		base = os.Getenv("GDX_REST_URL")
	}
	if base == "" {
		base = "http://127.0.0.1:4000"
	}

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKeyID:  keyID,
		APISecret: secret,
		BaseURL:   base,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Disconnect(ctx) }()

	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRestRequest{
		PlaceOrderRequest: godark.PlaceOrderRequest{
			Symbol:    symbol,
			Side:      godark.SideBuy,
			OrderType: godark.OrderTypeLimit,
			Quantity:  0.01,
			Price:     67_500,
		},
		ClientOrderID: "quickstart-docs-go",
	})
	if err != nil {
		envloader.PrintOrderError("PlaceOrder", err)
		os.Exit(1)
	}
	fmt.Printf("placed %s\n", ack.OrderID)
}
