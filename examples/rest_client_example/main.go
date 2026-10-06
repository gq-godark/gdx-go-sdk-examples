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
//	(GDX_API_KEY_ID, GDX_API_SECRET, GDX_PASSPHRASE are accepted too)
//	GODARK_REST_URL / GDX_REST_URL, or GODARK_EDGE_URL / GDX_EDGE_URL
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk-examples/examples/internal/envloader"
)

func main() {
	envloader.LoadDotenv()

	apiKeyID := envloader.First("GODARK_API_KEY_ID", "GDX_API_KEY_ID")
	apiSecret := envloader.First("GODARK_API_SECRET", "GDX_API_SECRET")
	passphrase := envloader.First("GODARK_PASSPHRASE", "GDX_PASSPHRASE")
	if apiKeyID == "" || apiSecret == "" || passphrase == "" {
		log.Fatal("Set GODARK_API_KEY_ID, GODARK_API_SECRET and GODARK_PASSPHRASE in .env or your environment")
	}

	cfg := godark.RestClientConfig{
		APIKeyID:   apiKeyID,
		APISecret:  apiSecret,
		Passphrase: passphrase,
		BaseURL: envloader.HTTPOrigin(envloader.First(
			"GODARK_REST_URL", "GDX_REST_URL", "GODARK_EDGE_URL", "GDX_EDGE_URL",
		)),
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
	fail := func(format string, args ...any) {
		_ = client.Disconnect(ctx)
		log.Fatalf(format, args...)
	}

	var positions *godark.PositionsSnapshot
	if err := retryRead(func() error {
		var err error
		positions, err = client.GetPositions(ctx)
		return err
	}); err != nil {
		fail("GetPositions: %v", err)
	}
	var orders *godark.OpenOrdersSnapshot
	if err := retryRead(func() error {
		var err error
		orders, err = client.GetOpenOrders(ctx)
		return err
	}); err != nil {
		fail("GetOpenOrders: %v", err)
	}
	var account *godark.AccountMarginUpdate
	if err := retryRead(func() error {
		var err error
		account, err = client.GetAccount(ctx)
		return err
	}); err != nil {
		fail("GetAccount: %v", err)
	}
	fmt.Printf("positions: %d rows\n", len(positions.Rows))
	fmt.Printf("open_orders: %d rows\n", len(orders.Rows))
	collateral := "?"
	if account.Account != nil {
		collateral = account.Account.TotalCollateral
	}
	fmt.Printf("account total_collateral=%s\n", collateral)
	if err := client.Disconnect(ctx); err != nil {
		log.Fatalf("Disconnect: %v", err)
	}

	fmt.Println("REST reads succeeded.")
	fmt.Println("For REST trading (place/modify/cancel), see full_trader_rest.")
}

func retryRead(fn func() error) error {
	var err error
	for i := 0; i < 3; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return err
}

func firstOrNil(rows []map[string]any) any {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}
