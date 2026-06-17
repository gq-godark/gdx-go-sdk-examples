// GoDark Go SDK -- Trader Reference Example
//
// Demonstrates:
//
//  1. Load credentials from `.env` / environment.
//  2. REST pre-flight: GetMe (identity + wallet) → GetMyBalance (margin check).
//  3. Connect and authenticate (encrypted ECDH WS session).
//  4. Wire up channel-first push receivers (order / position / health / etc.).
//  5. Subscribe to the private order + position channels.
//  6. Place, modify, and cancel `MARKET` / `LIMIT` orders.
//  7. Drain queued updates between actions.
//  8. REST post-flight: GetMyBalance (balance delta after trading).
//  9. Print a session summary including per-stream counts.
// 10. Clean disconnect.
//
// Run with:
//
//	go run ./examples/full_trader_example
//	# or, against the prebuilt bundle binary:
//	./full_trader_example
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk-examples/examples/internal/envloader"
)

const symbol = "BTC-USDC-PERP"

func main() {
	envloader.LoadDotenv()

	sep := strings.Repeat("=", 60)
	fmt.Println(sep)
	fmt.Println("  GoDark Go SDK -- Trader Reference Example")
	fmt.Println(sep)
	fmt.Println("Order-type support in this distribution: MARKET, LIMIT")

	apiKeyID := os.Getenv("GODARK_API_KEY_ID")
	apiSecret := os.Getenv("GODARK_API_SECRET")
	passphrase := os.Getenv("GODARK_PASSPHRASE")
	if apiKeyID == "" || apiSecret == "" || passphrase == "" {
		log.Fatal("Missing credentials. Set GODARK_API_KEY_ID, GODARK_API_SECRET and GODARK_PASSPHRASE (or provide them in .env).")
	}
	wsURL := os.Getenv("GODARK_EDGE_URL")
	if wsURL == "" {
		wsURL = "wss://api.godark-dex.com"
	}
	restURL := os.Getenv("GODARK_REST_URL")
	if restURL == "" {
		restURL = strings.Replace(strings.Replace(wsURL, "wss://", "https://", 1), "ws://", "http://", 1)
	}
	fmt.Printf("Endpoints: ws=%s  rest=%s\n", wsURL, restURL)

	ctx := context.Background()

	// --- REST pre-flight: identity + balance check ---
	rest, err := godark.NewRestClient(godark.RestClientConfig{
		APIKeyID:   apiKeyID,
		APISecret:  apiSecret,
		Passphrase: passphrase,
		BaseURL:    restURL,
	})
	if err != nil {
		log.Fatalf("REST config error: %v", err)
	}
	if err := rest.Connect(ctx); err != nil {
		log.Fatalf("REST connect failed: %v", err)
	}
	defer func() { _ = rest.Disconnect(ctx) }()

	me, err := rest.GetMe(ctx)
	if err != nil {
		log.Fatalf("GetMe: %v", err)
	}
	fmt.Printf("Identity: user_uuid=%s  wallet=%s\n", me.ID, me.WalletAddress)

	preBal, err := rest.GetMyBalance(ctx)
	if err != nil {
		log.Fatalf("GetMyBalance: %v", err)
	}
	fmt.Printf("Balance:  shielded_raw=%d  wallet_raw=%d  pending_deposits_raw=%d  wallet_ui=%.6f\n",
		preBal.ShieldedBalanceRaw, preBal.WalletUSDTRaw, preBal.PendingDepositsRaw, preBal.WalletUSDTUI)
	if preBal.ShieldedBalanceRaw == 0 {
		fmt.Println("No shielded balance -- deposit collateral before placing orders.")
		fmt.Println("Done.")
		return
	}

	// --- WS trading session ---
	headers := http.Header{}
	headers.Set("X-Trader-Tag", "go-full-trader-demo")

	client, err := godark.NewClient(godark.ClientConfig{
		APIKeyID:   apiKeyID,
		APISecret:  apiSecret,
		Passphrase: passphrase,
		BaseURL:    wsURL,
		Transport: godark.TransportConfig{
			Headers:           headers,
			HeartbeatInterval: 30 * time.Second,
			StaleTimeout:      60 * time.Second,
			CommandTimeout:    10 * time.Second,
		},
	})
	if err != nil {
		log.Fatalf("WS config error: %v", err)
	}

	if err := client.Connect(ctx); err != nil {
		log.Fatalf("WS connect failed: %v", err)
	}
	defer func() {
		_ = client.Disconnect()
		fmt.Println("Disconnected cleanly")
	}()

	fmt.Printf("WS authenticated as user_uuid=%s  (session encrypted)\n", client.UserUUID())

	if err := client.Subscribe(ctx, "orders", "positions"); err != nil {
		log.Fatalf("Subscribe failed: %v", err)
	}
	fmt.Println("Subscribed to order + position updates")

	// Initial settling window: the sequencer pushes a PositionsSnapshot
	// immediately after the trading session establishes.
	time.Sleep(200 * time.Millisecond)
	drainPositionUpdates(client)
	drainPositionsSnapshots(client)

	// Place a limit BUY.
	fmt.Println("Placing limit BUY @ 67500...")
	buyAck, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:    symbol,
		Side:      godark.SideBuy,
		OrderType: godark.OrderTypeLimit,
		Price:     67_500,
		Quantity:  0.1,
	})
	if err != nil {
		envloader.PrintOrderError("BUY rejected", err)
		os.Exit(1)
	}
	fmt.Printf("BUY placed: order_id=%s  sequence=%s\n", buyAck.OrderID, buyAck.Sequence)

	time.Sleep(1 * time.Second)
	drainOrderUpdates(client, "after BUY")

	// Modify the BUY price.
	fmt.Println("Modifying order price to 68000...")
	newPrice := 68_000.0
	if mAck, mErr := client.ModifyOrder(ctx, buyAck.OrderID, symbol, &newPrice, nil); mErr != nil {
		envloader.PrintOrderError("Modify rejected", mErr)
	} else {
		fmt.Printf("Modified: order_id=%s\n", mAck.OrderID)
	}

	time.Sleep(1 * time.Second)
	drainOrderUpdates(client, "after MODIFY")

	// Place + immediately cancel a SELL.
	fmt.Println("Placing limit SELL @ 95000...")
	if sellAck, sErr := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:    symbol,
		Side:      godark.SideSell,
		OrderType: godark.OrderTypeLimit,
		Price:     95_000,
		Quantity:  0.05,
	}); sErr != nil {
		envloader.PrintOrderError("SELL rejected", sErr)
	} else {
		fmt.Printf("SELL placed: order_id=%s\n", sellAck.OrderID)
		time.Sleep(500 * time.Millisecond)
		if cAck, cErr := client.CancelOrder(ctx, sellAck.OrderID, symbol); cErr != nil {
			envloader.PrintOrderError("Cancel SELL rejected", cErr)
		} else {
			fmt.Printf("SELL cancelled: order_id=%s\n", cAck.OrderID)
		}
	}

	time.Sleep(1 * time.Second)
	drainOrderUpdates(client, "after SELL/CANCEL")

	// --- Bulk quote (mass quote) ---
	// Place a whole ladder of resting quotes in one batched request. With the
	// default (post-only) mode -- pass nil for postOnly -- every leg is
	// post-only: a leg that would cross is rejected as "failed" so the batch
	// fuses into a single MPC round. Pass a *bool(false) for the relaxed path,
	// where a crossing leg takes liquidity up to its limit and rests the
	// remainder (the number of taker fills is reported per leg as FillCount).
	fmt.Println("Mass-quoting a 3-level BUY ladder (post-only)...")
	ladder := []godark.MassQuoteLegInput{
		{Side: godark.SideBuy, Price: 66_000, Quantity: 0.02},
		{Side: godark.SideBuy, Price: 65_500, Quantity: 0.02},
		{Side: godark.SideBuy, Price: 65_000, Quantity: 0.02},
	}
	var restingIDs []uint64
	if mq, mqErr := client.MassQuote(ctx, symbol, ladder, 1, nil); mqErr != nil {
		envloader.PrintOrderError("Mass quote rejected", mqErr)
	} else {
		fmt.Printf("Mass quote: success=%v  sequence=%s  legs=%d\n", mq.Success, mq.Sequence, len(mq.Results))
		for _, r := range mq.Results {
			errStr := "<nil>"
			if r.ErrorCode != nil {
				errStr = fmt.Sprintf("%d", *r.ErrorCode)
			}
			fmt.Printf("  leg %d: status=%s  new_order_id=%s  fills=%d  err=%s\n",
				r.LegIndex, r.Status, r.NewOrderID, r.FillCount, errStr)
			if r.Status == "open" && r.NewOrderID != "" {
				if id, perr := strconv.ParseUint(r.NewOrderID, 10, 64); perr == nil {
					restingIDs = append(restingIDs, id)
				}
			}
		}
	}

	time.Sleep(1 * time.Second)
	drainOrderUpdates(client, "after MASS QUOTE")

	if len(restingIDs) > 0 {
		fmt.Printf("Batch-cancelling %d ladder orders (cleanup)...\n", len(restingIDs))
		if bc, bcErr := client.BatchCancel(ctx, symbol, restingIDs); bcErr != nil {
			envloader.PrintOrderError("Batch cancel rejected", bcErr)
		} else {
			for _, r := range bc.Results {
				errStr := "<nil>"
				if r.ErrorCode != nil {
					errStr = fmt.Sprintf("%d", *r.ErrorCode)
				}
				fmt.Printf("  cancel id=%s: cancelled=%v  err=%s\n", r.OrderID, r.Cancelled, errStr)
			}
		}
		time.Sleep(500 * time.Millisecond)
		drainOrderUpdates(client, "after BATCH CANCEL")
	}

	// Cleanup: cancel the original BUY (if still resting).
	fmt.Println("Cancelling original BUY (cleanup)...")
	if _, err := client.CancelOrder(ctx, buyAck.OrderID, symbol); err != nil {
		fmt.Println("Original BUY already filled or cancelled")
	} else {
		fmt.Println("Original BUY cancelled")
	}

	// Drain anything that arrived during the session.
	snapCount := drainPositionsSnapshots(client)
	healthCount := drainHealth(client)
	balanceCount := drainBalances(client)
	marginCount := drainMargins(client)
	fundingCount := drainFunding(client)
	settleCount := drainSettlement(client)

	// --- REST post-flight: balance snapshot after trading ---
	if postBal, err := rest.GetMyBalance(ctx); err == nil {
		fmt.Printf("Balance after: shielded_raw=%d  (delta=%d)\n",
			postBal.ShieldedBalanceRaw,
			int64(postBal.ShieldedBalanceRaw)-int64(preBal.ShieldedBalanceRaw))
	}

	fmt.Println(sep)
	fmt.Println("  Session complete")
	fmt.Printf("  Pushes: snapshots=%d  health=%d  balance=%d  margin=%d  funding=%d  settle=%d\n",
		snapCount, healthCount, balanceCount, marginCount, fundingCount, settleCount)
	fmt.Println(sep)
}

// -----------------------------------------------------------------------
// Push-channel drainers
// -----------------------------------------------------------------------
//
// Each helper non-blockingly pulls everything currently buffered on the
// SDK's per-stream channel and prints it. The patterns are intentionally
// repetitive so the example reads like a recipe MMs can copy/paste.

func drainOrderUpdates(c *godark.GodarkClient, label string) {
	count := 0
	ch := c.OrderUpdates()
	for {
		select {
		case u := <-ch:
			count++
			fmt.Printf("ORDER  %s  id=%s  status=%s  filled=%s  remaining=%s\n",
				u.UpdateType, u.OrderID, u.Status, u.FilledQty, u.RemainingQty)
		default:
			if count > 0 {
				fmt.Printf("  (%d order update(s) %s)\n", count, label)
			}
			return
		}
	}
}

func drainPositionUpdates(c *godark.GodarkClient) int {
	count := 0
	ch := c.PositionUpdates()
	for {
		select {
		case p := <-ch:
			count++
			fmt.Printf("POS    side=%s  size=%s  entry=%s\n", p.Side, p.Size, p.EntryPrice)
		default:
			return count
		}
	}
}

func drainPositionsSnapshots(c *godark.GodarkClient) int {
	count := 0
	ch := c.PositionsSnapshots()
	for {
		select {
		case s := <-ch:
			count++
			fmt.Printf("SNAP   source=%s  rows=%d  ts=%d\n",
				s.Source, len(s.Rows), s.ServerTimestamp)
			for _, row := range s.Rows {
				mark := row.MarkPrice
				if mark == "" {
					mark = "—"
				}
				fmt.Printf("  -> symbol=%d  side=%s  size=%s  entry=%s  mark=%s\n",
					row.SymbolID, row.Side, row.Size, row.EntryPrice, mark)
			}
		default:
			return count
		}
	}
}

func drainHealth(c *godark.GodarkClient) int {
	count := 0
	ch := c.SystemHealthUpdates()
	for {
		select {
		case h := <-ch:
			count++
			fmt.Printf("HEALTH nodes=%d  accepting=%v  ready=%v\n",
				h.TotalNodes, h.AcceptingOrders, h.Ready)
		default:
			return count
		}
	}
}

func drainBalances(c *godark.GodarkClient) int {
	count := 0
	ch := c.BalanceUpdates()
	for {
		select {
		case b := <-ch:
			count++
			fmt.Printf("BAL    shielded_raw=%d\n", b.ShieldedBalanceRaw)
		default:
			return count
		}
	}
}

func drainMargins(c *godark.GodarkClient) int {
	count := 0
	ch := c.MarginAlerts()
	for {
		select {
		case a := <-ch:
			count++
			fmt.Printf("MARGIN symbol=%d  tier=%d  ratio_bps=%d\n",
				a.SymbolID, a.Tier, a.MarginRatioBps)
		default:
			return count
		}
	}
}

func drainFunding(c *godark.GodarkClient) int {
	count := 0
	ch := c.FundingRateUpdates()
	for {
		select {
		case f := <-ch:
			count++
			fmt.Printf("FUND   symbol=%d  current=%s  predicted=%s\n",
				f.SymbolID, f.CurrentRate, f.PredictedRate)
		default:
			return count
		}
	}
}

func drainSettlement(c *godark.GodarkClient) int {
	count := 0
	ch := c.SettlementUpdates()
	for {
		select {
		case s := <-ch:
			count++
			fmt.Printf("SETTLE batch=%d  status=%s\n", s.BatchID, s.Status)
		default:
			return count
		}
	}
}
