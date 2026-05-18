// GoDark Go SDK -- Local docs-wire encrypted trading probe
//
// Connects with docs-wire envelope (default), against the localnet edge
// using a static api_key (`test-key-1`) by default. Reports each command
// result with the SDK's OrderError symbolic code when applicable.
//
// Run with:
//
//	go run ./examples/docs_ws_trade
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk/examples/internal/envloader"
)

const symbol = "BTC-USDC-PERP"

func main() {
	envloader.LoadDotenv()

	token := os.Getenv("GODARK_AUTH_TOKEN")
	if token == "" {
		token = os.Getenv("GDX_AUTH_TOKEN")
	}
	if token == "" {
		token = "test-key-1"
	}
	baseURL := os.Getenv("GODARK_EDGE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("GDX_EDGE_URL")
	}
	if baseURL == "" {
		baseURL = "ws://127.0.0.1:4000"
	}
	userUUID := os.Getenv("GODARK_USER_UUID")
	if userUUID == "" {
		userUUID = "00000000-0000-4000-8000-000000000001"
	}

	client, err := godark.NewClient(godark.ClientConfig{
		APIKey:   token,
		BaseURL:  baseURL,
		UserUUID: userUUID,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Disconnect() }()
	fmt.Printf("connected %s\n", client.UserUUID())

	if err := client.Subscribe(ctx, "orders", "positions"); err != nil {
		log.Fatal(err)
	}
	fmt.Println("subscribed")

	report("order.place", func() (any, error) {
		return client.PlaceOrder(ctx, godark.PlaceOrderRequest{
			Symbol:    symbol,
			Side:      godark.SideBuy,
			OrderType: godark.OrderTypeLimit,
			Quantity:  0.001,
			Price:     1.0,
		})
	})
	newPrice := 2.0
	report("order.modify", func() (any, error) {
		return client.ModifyOrder(ctx, "999999999", symbol, &newPrice, nil)
	})
	report("order.cancel", func() (any, error) {
		return client.CancelOrder(ctx, "999999999", symbol)
	})
}

func report(name string, fn func() (any, error)) {
	v, err := fn()
	if err == nil {
		fmt.Printf("%s OK %+v\n", name, v)
		return
	}
	var oe *godark.OrderError
	if errors.As(err, &oe) {
		fmt.Printf("%s ORDER_ERROR %s %s\n", name, oe.Error(), oe.ErrorCode)
		return
	}
	fmt.Printf("%s ERROR %v\n", name, err)
}
