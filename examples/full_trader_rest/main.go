// REST-only trader demo — auth + encrypted snapshots + place/modify/cancel.
//
// Prices and sizes are decimal strings only (not float64). Do not set
// ClientOrderID: REST place does not register it. Registration happens only
// after a successful WebSocket place, and only on HTTP 200.
//
// The limit is a post-only buy at least 500 below the live mark, size 0.001.
// GODARK_EDGE_URL / GDX_EDGE_URL (or the REST URL aliases) select the host.
// There is no hardcoded mark and no fallback to the public testnet host when
// an edge URL is set.
//
//	GODARK_EDGE_URL=https://api.devnet.godark-dex.com \
//	GODARK_API_KEY_ID=... GODARK_API_SECRET=... GODARK_PASSPHRASE=... \
//	GODARK_ACCOUNT=<Solana base58 account> \
//	  go run ./examples/full_trader_rest
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk-examples/examples/internal/envloader"
)

const (
	symbol     = "BTC-USDC-PERP"
	cancelWait = 1200 * time.Millisecond
)

func main() {
	envloader.LoadDotenv()

	base := envloader.HTTPOrigin(envloader.First(
		"GODARK_REST_URL", "GDX_REST_URL", "GODARK_EDGE_URL", "GDX_EDGE_URL",
	))
	if base == "" {
		log.Fatal("Set GODARK_EDGE_URL or GDX_EDGE_URL (or GODARK_REST_URL / GDX_REST_URL)")
	}

	keyID := envloader.First("GODARK_API_KEY_ID", "GDX_API_KEY_ID")
	secret := envloader.First("GODARK_API_SECRET", "GDX_API_SECRET")
	passphrase := envloader.First("GODARK_PASSPHRASE", "GDX_PASSPHRASE")
	legacyKey := envloader.First("GODARK_API_KEY", "GDX_API_KEY")

	cfg := godark.RestClientConfig{
		BaseURL: base,
		Account: envloader.First("GODARK_ACCOUNT", "GDX_ACCOUNT"),
	}
	if keyID != "" && secret != "" {
		cfg.APIKeyID = keyID
		cfg.APISecret = secret
		cfg.Passphrase = passphrase
	} else if legacyKey != "" {
		cfg.APIKey = legacyKey
	} else {
		log.Fatal("Set GODARK_API_KEY_ID, GODARK_API_SECRET and GODARK_PASSPHRASE in .env")
	}

	client, err := godark.NewRestClient(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}

	code := run(ctx, client)
	if err := client.Disconnect(ctx); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "disconnect: %v\n", err)
		code = 1
	}
	if code != 0 {
		os.Exit(code)
	}
}

func run(ctx context.Context, client *godark.GodarkRestClient) int {
	fmt.Printf("connected account=%s\n", client.Account())

	var open *godark.OpenOrdersSnapshot
	if err := retryRead(func() error {
		var err error
		open, err = client.GetOpenOrders(ctx)
		return err
	}); err != nil {
		fmt.Fprintf(os.Stderr, "GetOpenOrders: %v\n", err)
		return 1
	}
	fmt.Println("open_orders", len(open.Rows))

	var pos *godark.PositionsSnapshot
	if err := retryRead(func() error {
		var err error
		pos, err = client.GetPositions(ctx)
		return err
	}); err != nil {
		fmt.Fprintf(os.Stderr, "GetPositions: %v\n", err)
		return 1
	}
	fmt.Println("positions", len(pos.Rows))

	var acct *godark.AccountMarginUpdate
	if err := retryRead(func() error {
		var err error
		acct, err = client.GetAccount(ctx)
		return err
	}); err != nil {
		fmt.Fprintf(os.Stderr, "GetAccount: %v\n", err)
		return 1
	}
	if acct.Account != nil {
		fmt.Println("account total_collateral=", acct.Account.TotalCollateral)
	}

	oi, oiErr := client.GetOpenInterest(ctx)
	if oiErr != nil {
		oi = nil
	}
	mark, err := envloader.ResolveMark(0, oi)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live mark: %v\n", err)
		return 1
	}
	limitPrice, err := envloader.PostOnlyBuy(mark, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "buy price: %v\n", err)
		return 1
	}
	fmt.Printf("live mark=%.4f  post-only buy=%s qty=%s\n", mark, limitPrice, envloader.DemoQuantity)

	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRestRequest{
		PlaceOrderRequest: godark.PlaceOrderRequest{
			Symbol:    symbol,
			Side:      godark.SideBuy,
			OrderType: godark.OrderTypeLimit,
			Quantity:  envloader.DemoQuantity,
			Price:     limitPrice,
			Options:   godark.PlaceOrderOptions{PostOnly: true},
		},
	})
	if err != nil {
		envloader.PrintOrderError("PlaceOrder", err)
		return 1
	}
	fmt.Println("placed order_id=", ack.OrderID, "success=", ack.Success)
	placedAt := time.Now()

	if wait := cancelWait - time.Since(placedAt); wait > 0 {
		time.Sleep(wait)
	}
	newPrice, err := envloader.PostOnlyBuy(mark, 1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modify price: %v\n", err)
		cancelOwn(ctx, client, ack.OrderID, placedAt)
		return 1
	}
	mod, err := client.ModifyOrder(ctx, ack.OrderID, symbol, &newPrice, nil, nil)
	if err != nil {
		envloader.PrintOrderError("ModifyOrder", err)
		if cerr := cancelOwn(ctx, client, ack.OrderID, placedAt); cerr != nil {
			envloader.PrintOrderError("CancelOrder", cerr)
		}
		return 1
	}
	fmt.Println("modified success=", mod.Success, "price=", newPrice)

	if err := cancelOwn(ctx, client, ack.OrderID, placedAt); err != nil {
		envloader.PrintOrderError("CancelOrder", err)
		return 1
	}
	return 0
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

func cancelOwn(ctx context.Context, client *godark.GodarkRestClient, orderID string, placedAt time.Time) error {
	if wait := cancelWait - time.Since(placedAt); wait > 0 {
		time.Sleep(wait)
	}
	can, err := client.CancelOrder(ctx, orderID, symbol)
	if err != nil {
		return err
	}
	fmt.Println("cancelled success=", can.Success, "order_id=", orderID)
	return nil
}
