// GoDark Go SDK -- Quickstart Example
//
// Place a post-only limit sell at least 500 above the live mark, wait, then
// cancel that order. Connect mints a REST access token and uses that token
// for the WebSocket login. Prices and sizes are strings. ClientOrderID, if
// set, is registered only after this place succeeds and only when
// POST /orders/_register_coid returns HTTP 200.
//
// Prices and sizes are decimal strings only (not float64). The limit is
// formatted from the live mark (or GDX_LIVE_PRICE / GODARK_E2E_PRICE when
// set). Size is 0.001.
//
// Reads credentials from `.env` (or the OS environment):
//
//	GODARK_API_KEY_ID=gdk_...   (GDX_API_KEY_ID also accepted)
//	GODARK_API_SECRET=...
//	GODARK_PASSPHRASE=...
//	GODARK_ACCOUNT=<Solana base58 account>
//	# GODARK_EDGE_URL=...   (optional; GDX_EDGE_URL also accepted)
//
// Run with:
//
//	go run ./examples/quickstart
//	# or, against the prebuilt bundle binary:
//	./quickstart
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

	legacyKey := envloader.First("GODARK_API_KEY", "GDX_API_KEY")
	edge := envloader.First("GODARK_EDGE_URL", "GDX_EDGE_URL")

	cfg := godark.ClientConfig{
		Environment: godark.EnvironmentTestnet,
		BaseURL:     envloader.WSOrigin(edge),
		Account:     envloader.First("GODARK_ACCOUNT", "GDX_ACCOUNT"),
	}
	restCfg := godark.RestClientConfig{
		BaseURL: envloader.HTTPOrigin(edge),
		Account: cfg.Account,
	}
	if legacyKey != "" {
		cfg.APIKey = legacyKey
		cfg.UserUUID = envloader.First("GODARK_USER_UUID", "GDX_USER_UUID")
		restCfg.APIKey = legacyKey
	} else {
		apiKeyID := envloader.First("GODARK_API_KEY_ID", "GDX_API_KEY_ID")
		apiSecret := envloader.First("GODARK_API_SECRET", "GDX_API_SECRET")
		passphrase := envloader.First("GODARK_PASSPHRASE", "GDX_PASSPHRASE")
		if apiKeyID == "" || apiSecret == "" || passphrase == "" {
			log.Fatal("Set GODARK_API_KEY_ID/GODARK_API_SECRET/GODARK_PASSPHRASE or legacy GODARK_API_KEY")
		}
		cfg.APIKeyID = apiKeyID
		cfg.APISecret = apiSecret
		cfg.Passphrase = passphrase
		restCfg.APIKeyID = apiKeyID
		restCfg.APISecret = apiSecret
		restCfg.Passphrase = passphrase
	}

	ctx := context.Background()
	mark, err := liveMark(ctx, restCfg, 0)
	if err != nil {
		log.Fatal(err)
	}
	sellPx, err := envloader.PostOnlySell(mark)
	if err != nil {
		log.Fatal(err)
	}

	client, err := godark.NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}

	code := run(ctx, client, sellPx, mark)
	if err := client.Disconnect(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "disconnect: %v\n", err)
		code = 1
	}
	if code != 0 {
		os.Exit(code)
	}
}

func run(ctx context.Context, client *godark.GodarkClient, sellPx string, mark float64) int {
	fmt.Printf("Connected as account %s\n", client.Account())
	fmt.Printf("live mark=%.4f  post-only sell=%s qty=%s\n", mark, sellPx, envloader.DemoQuantity)

	// Book confirmation waits on order-channel pushes; subscribe first.
	if err := client.Subscribe(ctx, "orders"); err != nil {
		fmt.Fprintf(os.Stderr, "Subscribe: %v\n", err)
		return 1
	}

	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:    symbol,
		Side:      godark.SideSell,
		OrderType: godark.OrderTypeLimit,
		Price:     sellPx,
		Quantity:  envloader.DemoQuantity,
		Options:   godark.PlaceOrderOptions{PostOnly: true},
	})
	if err != nil {
		envloader.PrintOrderError("PlaceOrder", err)
		return 1
	}
	fmt.Printf("Place OK -- order_id=%s (post-only limit SELL @ %s)\n", ack.OrderID, sellPx)
	placedAt := time.Now()

	if wait := cancelWait - time.Since(placedAt); wait > 0 {
		time.Sleep(wait)
	}
	cancelAck, err := client.CancelOrder(ctx, ack.OrderID, symbol)
	if err != nil {
		envloader.PrintOrderError("CancelOrder", err)
		time.Sleep(cancelWait)
		cancelAck, err = client.CancelOrder(ctx, ack.OrderID, symbol)
		if err != nil {
			envloader.PrintOrderError("CancelOrder retry", err)
			return 1
		}
	}
	fmt.Printf("cancel OK -- order_id=%s\n", cancelAck.OrderID)
	return 0
}

func liveMark(ctx context.Context, cfg godark.RestClientConfig, snapshot float64) (float64, error) {
	rest, err := godark.NewRestClient(cfg)
	if err != nil {
		return 0, err
	}
	oi, err := rest.GetOpenInterest(ctx)
	if err != nil {
		oi = nil
	}
	return envloader.ResolveMark(snapshot, oi)
}
