// GoDark Go SDK -- Quickstart Example
//
// Place a limit sell, then cancel it. The minimal happy path against the
// encrypted WebSocket trading client.
//
// Reads credentials from `.env` (or the OS environment):
//
//	GODARK_API_KEY_ID=gdk_...
//	GODARK_API_SECRET=...
//	GODARK_PASSPHRASE=...
//	# GODARK_EDGE_URL=...   (optional; default EnvironmentTestnet)
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
	"math"
	"os"
	"strconv"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk-examples/examples/internal/envloader"
)

const symbol = "BTC-USDC-PERP"

func liveMarkPrice() float64 {
	if raw := envloader.First("GODARK_E2E_PRICE", "GDX_E2E_PRICE", "GDX_LIVE_PRICE"); raw != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return f
		}
	}
	return 79000.0
}

func main() {
	envloader.LoadDotenv()

	legacyKey := envloader.First("GODARK_API_KEY", "GDX_API_KEY")
	baseURL := envloader.First("GODARK_EDGE_URL", "GDX_EDGE_URL")

	cfg := godark.ClientConfig{
		Environment: godark.EnvironmentTestnet,
		BaseURL:     baseURL,
	}
	if legacyKey != "" {
		cfg.APIKey = legacyKey
		cfg.UserUUID = envloader.First("GODARK_USER_UUID", "GDX_USER_UUID")
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
	}

	client, err := godark.NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer func() {
		_ = client.Disconnect()
	}()

	fmt.Printf("Connected as user %s\n", client.UserUUID())

	// Book confirmation waits on order-channel pushes; subscribe first.
	if err := client.Subscribe(ctx, "orders"); err != nil {
		log.Fatal(err)
	}

	mark := liveMarkPrice()
	sellPx := math.Round(mark*1.03*10) / 10
	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:    symbol,
		Side:      godark.SideSell,
		OrderType: godark.OrderTypeLimit,
		Price:     sellPx,
		Quantity:  0.01,
		Options:   godark.PlaceOrderOptions{PostOnly: true},
		// Empty Confirmation => Book (waits for OPEN after subscribe).
	})
	if err != nil {
		envloader.PrintOrderError("PlaceOrder", err)
		os.Exit(1)
	}
	fmt.Printf("Place OK -- order_id=%s (limit SELL @ %.1f, mark=%.1f)\n", ack.OrderID, sellPx, mark)

	// Allow the resting order to settle before cancel (avoids CANCEL_TOO_SOON).
	time.Sleep(500 * time.Millisecond)

	cancelAck, err := client.CancelAllOrders(ctx, symbol)
	if err != nil {
		envloader.PrintOrderError("CancelAllOrders", err)
		os.Exit(1)
	}
	fmt.Printf("cancel_all OK -- count=%d ids=%v\n", cancelAck.Count, cancelAck.OrderIDs)

	fmt.Println("Disconnected")
}
