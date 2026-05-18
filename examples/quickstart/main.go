// GoDark Go SDK -- Quickstart Example
//
// Place a limit sell, then cancel it. The minimal happy path against the
// encrypted WebSocket trading client.
//
// Reads credentials from `.env` (or the OS environment):
//
//	GODARK_API_KEY_ID=gdk_...
//	GODARK_API_SECRET=...
//	# GODARK_EDGE_URL=wss://api.godark-dex.com   (optional override)
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

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk-examples/examples/internal/envloader"
)

const symbol = "BTC-USDC-PERP"

func main() {
	envloader.LoadDotenv()

	apiKeyID := os.Getenv("GODARK_API_KEY_ID")
	apiSecret := os.Getenv("GODARK_API_SECRET")
	baseURL := os.Getenv("GODARK_EDGE_URL")
	if apiKeyID == "" || apiSecret == "" {
		log.Fatal("Set GODARK_API_KEY_ID and GODARK_API_SECRET in .env or your environment")
	}

	client, err := godark.NewClient(godark.ClientConfig{
		APIKeyID:  apiKeyID,
		APISecret: apiSecret,
		BaseURL:   baseURL, // empty => SDK default
	})
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

	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:    symbol,
		Side:      godark.SideSell,
		OrderType: godark.OrderTypeLimit,
		Price:     999_999,
		Quantity:  0.01,
	})
	if err != nil {
		envloader.PrintOrderError("PlaceOrder", err)
		os.Exit(1)
	}
	fmt.Printf("Place OK -- order_id=%s\n", ack.OrderID)

	cancelAck, err := client.CancelOrder(ctx, ack.OrderID, symbol)
	if err != nil {
		envloader.PrintOrderError("CancelOrder", err)
		os.Exit(1)
	}
	fmt.Printf("Cancel OK -- order_id=%s\n", cancelAck.OrderID)

	fmt.Println("Disconnected")
}
