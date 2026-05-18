// GoDark Go SDK -- Local e2e smoke test
//
// Connects to a local edge instance using api_key auth (legacy-wire
// envelope, not docs-wire) and exercises a full place + drain + cancel.
//
// Run with:
//
//	GODARK_EDGE_URL=ws://localhost:4000 go run ./examples/local_e2e
//
// Exit codes:
//   0 success / partial      1 config       2 connect/auth/subscribe
//   3 place failed           4 cancel failed
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk/examples/internal/envloader"
)

func main() {
	envloader.LoadDotenv()

	apiKey := os.Getenv("GODARK_API_KEY")
	if apiKey == "" {
		apiKey = "test-key-1"
	}
	edgeURL := os.Getenv("GODARK_EDGE_URL")
	if edgeURL == "" {
		edgeURL = "ws://localhost:4000"
	}
	userUUID := os.Getenv("GODARK_USER_UUID")
	if userUUID == "" {
		userUUID = "00000000-0000-4000-8000-000000000001"
	}

	fmt.Fprintf(os.Stderr, "[local_e2e] api_key=%s  edge_url=%s  user_uuid=%s\n",
		apiKey, edgeURL, userUUID)

	t0 := time.Now()

	client, err := godark.NewClient(godark.ClientConfig{
		APIKey:   apiKey,
		BaseURL:  edgeURL,
		UserUUID: userUUID,
		Transport: godark.TransportConfig{
			LegacyWire: true,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[local_e2e] config error: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	fmt.Fprintln(os.Stderr, "[local_e2e] Connecting ...")
	if err := client.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "[local_e2e] connect failed: %v\n", err)
		os.Exit(2)
	}
	defer func() { _ = client.Disconnect() }()

	fmt.Fprintf(os.Stderr, "[local_e2e] Auth + ECDH OK -- user_uuid=%s (%d ms)\n",
		client.UserUUID(), time.Since(t0).Milliseconds())

	if err := client.Subscribe(ctx, "orders", "positions"); err != nil {
		fmt.Fprintf(os.Stderr, "[local_e2e] subscribe failed: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "[local_e2e] Subscribed to orders + positions")

	const (
		symbol = "BTC-USDC-PERP"
		qty    = 0.01
		price  = 10_000.0
	)
	fmt.Fprintf(os.Stderr, "[local_e2e] Placing LIMIT BUY %v %s @ %v ...\n", qty, symbol, price)

	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:    symbol,
		Side:      godark.SideBuy,
		OrderType: godark.OrderTypeLimit,
		Quantity:  qty,
		Price:     price,
	})
	if err != nil {
		envloader.PrintOrderError("place_order", err)
		os.Exit(3)
	}
	fmt.Fprintf(os.Stderr, "[local_e2e] Place OK -- order_id=%s sequence=%s\n",
		ack.OrderID, ack.Sequence)

	drainOrderUpdates(client, 5*time.Second, 5)

	fmt.Fprintf(os.Stderr, "[local_e2e] Cancelling order %s ...\n", ack.OrderID)
	cancelAck, err := client.CancelOrder(ctx, ack.OrderID, symbol)
	if err != nil {
		envloader.PrintOrderError("cancel_order", err)
		os.Exit(4)
	}
	fmt.Fprintf(os.Stderr, "[local_e2e] Cancel OK -- order_id=%s sequence=%s\n",
		cancelAck.OrderID, cancelAck.Sequence)

	drainOrderUpdates(client, 3*time.Second, 5)

	fmt.Fprintf(os.Stderr, "[local_e2e] PASSED -- full encrypted trading path validated (%d ms)\n",
		time.Since(t0).Milliseconds())
}

func drainOrderUpdates(c *godark.GodarkClient, max time.Duration, limit int) {
	ch := c.OrderUpdates()
	deadline := time.After(max)
	count := 0
	for count < limit {
		select {
		case u, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr,
				"[local_e2e] OrderUpdate: type=%s order_id=%s status=%s filled=%s\n",
				u.UpdateType, u.OrderID, u.Status, u.FilledQty)
			count++
		case <-deadline:
			return
		}
	}
}
