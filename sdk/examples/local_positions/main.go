// GoDark Go SDK -- Two-user position test
//
// Spins up two GodarkClient instances (buyer + seller), places crossing
// LIMIT orders at the same price, then drains order + position updates
// for both users to confirm the fill propagates as a PositionUpdate.
//
// Run with (against a localnet edge that issues `test-key-1` /
// `test-key-2` static keys):
//
//	go run ./examples/local_positions
//
// Exit codes:
//   0 both users got position updates    1 config       2 connect/subscribe
//   3 place failed                       5 partial (only one user got pos)
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk/examples/internal/envloader"
)

const (
	edgeURL = "ws://localhost:4000"
	symbol  = "BTC-USDC-PERP"
	qty     = 0.01
	price   = 50_000.0
)

func main() {
	envloader.LoadDotenv()

	t0 := time.Now()

	buyer, err := makeClient("test-key-1", "00000000-0000-4000-8000-000000000001")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[positions] buyer config error: %v\n", err)
		os.Exit(1)
	}
	seller, err := makeClient("test-key-2", "00000000-0000-4000-8000-000000000002")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[positions] seller config error: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()

	fmt.Fprintln(os.Stderr, "[positions] Connecting buyer ...")
	if err := buyer.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "[positions] buyer connect failed: %v\n", err)
		os.Exit(2)
	}
	defer func() { _ = buyer.Disconnect() }()

	fmt.Fprintln(os.Stderr, "[positions] Connecting seller ...")
	if err := seller.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "[positions] seller connect failed: %v\n", err)
		os.Exit(2)
	}
	defer func() { _ = seller.Disconnect() }()

	fmt.Fprintf(os.Stderr, "[positions] Both connected (%d ms)\n",
		time.Since(t0).Milliseconds())

	if err := buyer.Subscribe(ctx, "orders", "positions"); err != nil {
		fmt.Fprintf(os.Stderr, "[positions] buyer subscribe failed: %v\n", err)
		os.Exit(2)
	}
	if err := seller.Subscribe(ctx, "orders", "positions"); err != nil {
		fmt.Fprintf(os.Stderr, "[positions] seller subscribe failed: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "[positions] Both subscribed to orders + positions")

	fmt.Fprintf(os.Stderr, "[positions] Buyer placing LIMIT BUY %v %s @ %v ...\n", qty, symbol, price)
	buyAck, err := buyer.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol: symbol, Side: godark.SideBuy, OrderType: godark.OrderTypeLimit,
		Quantity: qty, Price: price,
	})
	if err != nil {
		envloader.PrintOrderError("buy", err)
		os.Exit(3)
	}
	fmt.Fprintf(os.Stderr, "[positions] Buy placed -- order_id=%s\n", buyAck.OrderID)

	fmt.Fprintf(os.Stderr, "[positions] Seller placing LIMIT SELL %v %s @ %v ...\n", qty, symbol, price)
	sellAck, err := seller.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol: symbol, Side: godark.SideSell, OrderType: godark.OrderTypeLimit,
		Quantity: qty, Price: price,
	})
	if err != nil {
		envloader.PrintOrderError("sell", err)
		os.Exit(3)
	}
	fmt.Fprintf(os.Stderr, "[positions] Sell placed -- order_id=%s\n", sellAck.OrderID)

	gotBuyer, gotSeller := drainBoth(buyer, seller, 10*time.Second)

	_, _ = buyer.CancelOrder(ctx, buyAck.OrderID, symbol)
	_, _ = seller.CancelOrder(ctx, sellAck.OrderID, symbol)

	totalMS := time.Since(t0).Milliseconds()
	if gotBuyer && gotSeller {
		fmt.Fprintf(os.Stderr, "[positions] PASSED -- position updates received for both users (%d ms)\n", totalMS)
		return
	}
	fmt.Fprintf(os.Stderr,
		"[positions] PARTIAL -- buyer_position=%v seller_position=%v (%d ms)\n",
		gotBuyer, gotSeller, totalMS)
	os.Exit(5)
}

func makeClient(apiKey, userUUID string) (*godark.GodarkClient, error) {
	return godark.NewClient(godark.ClientConfig{
		APIKey:   apiKey,
		BaseURL:  edgeURL,
		UserUUID: userUUID,
	})
}

func drainBoth(buyer, seller *godark.GodarkClient, max time.Duration) (bool, bool) {
	deadline := time.After(max)
	bo, so := buyer.OrderUpdates(), seller.OrderUpdates()
	bp, sp := buyer.PositionUpdates(), seller.PositionUpdates()
	gotBuyer, gotSeller := false, false
	for !(gotBuyer && gotSeller) {
		select {
		case <-deadline:
			return gotBuyer, gotSeller
		case u, ok := <-bo:
			if !ok {
				return gotBuyer, gotSeller
			}
			fmt.Fprintf(os.Stderr,
				"[positions] Buyer Order: type=%s oid=%s status=%s filled=%s\n",
				u.UpdateType, u.OrderID, u.Status, u.FilledQty)
		case u, ok := <-so:
			if !ok {
				return gotBuyer, gotSeller
			}
			fmt.Fprintf(os.Stderr,
				"[positions] Seller Order: type=%s oid=%s status=%s filled=%s\n",
				u.UpdateType, u.OrderID, u.Status, u.FilledQty)
		case p, ok := <-bp:
			if !ok {
				return gotBuyer, gotSeller
			}
			fmt.Fprintf(os.Stderr,
				"[positions] Buyer Position: type=%s symbol=%d side=%s size=%s entry=%s fill=%s qty=%s\n",
				p.UpdateType, p.SymbolID, p.Side, p.Size, p.EntryPrice, p.FillPrice, p.FillQty)
			gotBuyer = true
		case p, ok := <-sp:
			if !ok {
				return gotBuyer, gotSeller
			}
			fmt.Fprintf(os.Stderr,
				"[positions] Seller Position: type=%s symbol=%d side=%s size=%s entry=%s fill=%s qty=%s\n",
				p.UpdateType, p.SymbolID, p.Side, p.Size, p.EntryPrice, p.FillPrice, p.FillQty)
			gotSeller = true
		}
	}
	return gotBuyer, gotSeller
}
