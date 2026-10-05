// GoDark Go SDK — minimal GodarkRestClient demo.
//
// Auth + account reads + public market-data GETs. For encrypted place/modify/
// cancel over REST (one-shot HPKE), see full_trader_rest. REST place does
// not register a client-order id.
//
//	go run ./examples/rest_client_example
//
// Environment:
//
//	GODARK_API_KEY_ID, GODARK_API_SECRET, GODARK_PASSPHRASE
//	GODARK_REST_URL (optional; default https://api.godark-dex.com)
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk-examples/examples/internal/envloader"
)

func main() {
	envloader.LoadDotenv()

	apiKeyID := os.Getenv("GODARK_API_KEY_ID")
	apiSecret := os.Getenv("GODARK_API_SECRET")
	passphrase := os.Getenv("GODARK_PASSPHRASE")
	if apiKeyID == "" || apiSecret == "" || passphrase == "" {
		log.Fatal("Set GODARK_API_KEY_ID, GODARK_API_SECRET and GODARK_PASSPHRASE in .env or your environment")
	}

	cfg := godark.RestClientConfig{
		APIKeyID:   apiKeyID,
		APISecret:  apiSecret,
		Passphrase: passphrase,
		BaseURL:    os.Getenv("GODARK_REST_URL"),
	}
	client, err := godark.NewRestClient(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	// Public market-data GETs — Connect not required.
	rates, err := client.GetFundingRates(ctx)
	if err != nil {
		log.Fatalf("GetFundingRates: %v", err)
	}
	oi, err := client.GetOpenInterest(ctx)
	if err != nil {
		log.Fatalf("GetOpenInterest: %v", err)
	}
	vol, err := client.GetVolume(ctx)
	if err != nil {
		log.Fatalf("GetVolume: %v", err)
	}
	fmt.Printf("funding_rates: %d symbols (first=%v)\n", len(rates), firstOrNil(rates))
	fmt.Printf("open_interest: %d symbols (first=%v)\n", len(oi), firstOrNil(oi))
	syms, _ := vol["symbols"].([]any)
	fmt.Printf("volume: total_24h=%v symbols=%d\n", vol["total_volume_24h"], len(syms))

	fmt.Println("connecting (REST auth/token)...")
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Disconnect(ctx) }()

	positions, err := client.GetPositions(ctx)
	if err != nil {
		log.Fatalf("GetPositions: %v", err)
	}
	orders, err := client.GetOpenOrders(ctx)
	if err != nil {
		log.Fatalf("GetOpenOrders: %v", err)
	}
	account, err := client.GetAccount(ctx)
	if err != nil {
		log.Fatalf("GetAccount: %v", err)
	}
	fmt.Printf("positions: %d rows\n", len(positions.Rows))
	fmt.Printf("open_orders: %d rows\n", len(orders.Rows))
	collateral := "?"
	if account.Account != nil {
		collateral = account.Account.TotalCollateral
	}
	fmt.Printf("account total_collateral=%s\n", collateral)

	fmt.Println("REST reads succeeded.")
	fmt.Println("For REST trading (place/modify/cancel), see full_trader_rest.")
}

func firstOrNil(rows []map[string]any) any {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}
