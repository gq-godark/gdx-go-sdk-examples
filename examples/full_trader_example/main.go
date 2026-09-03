// GoDark Go SDK -- Trader Reference Example
//
// Demonstrates:
//
//  1. Load credentials from `.env` / environment.
//  2. Connect and authenticate (HPKE WebSocket session).
//  3. Wire up channel-first push receivers (order / position / health / etc.).
//  4. Subscribe to the private order + position channels.
//  5. Place, modify, and cancel `MARKET` / `LIMIT` orders.
//  6. Drain queued updates between actions.
//  7. Print a session summary including per-stream counts.
//  8. Clean disconnect.
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
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
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

// btcSymbolID is BTC-USDC-PERP's numeric symbol id in position snapshots.
const btcSymbolID = 1

// lastBtcMark holds the most recent BTC mark seen in a positions snapshot, so
// the mass-quote ladder/cross prices can anchor to the live touch. Zero means
// "not seen yet" (fall back to GDX_BASE).
var lastBtcMark float64

var leverageCount int

func main() {
	envloader.LoadDotenv()

	sep := strings.Repeat("=", 60)
	fmt.Println(sep)
	fmt.Println("  GoDark Go SDK -- Trader Reference Example")
	fmt.Println(sep)
	fmt.Println("Order-type support in this distribution: MARKET, LIMIT")

	legacyKey := envloader.First("GODARK_API_KEY", "GDX_API_KEY")
	wsURL := envloader.First("GODARK_EDGE_URL", "GDX_EDGE_URL")
	if wsURL == "" {
		wsURL = godark.EnvironmentTestnet.EdgeBaseURL()
	}
	fmt.Printf("Endpoint: ws=%s\n", wsURL)

	ctx := context.Background()

	// --- WS trading session ---
	headers := http.Header{}
	headers.Set("X-Trader-Tag", "go-full-trader-demo")

	cfg := godark.ClientConfig{
		Environment: godark.EnvironmentTestnet,
		BaseURL:     envloader.First("GODARK_EDGE_URL", "GDX_EDGE_URL"), // empty => Testnet preset
		Transport: godark.TransportConfig{
			Headers:              headers,
			HeartbeatInterval:    30 * time.Second,
			StaleTimeout:         120 * time.Second,
			MissedHeartbeatLimit: 2,
			CommandTimeout:       10 * time.Second,
		},
	}
	if legacyKey != "" {
		cfg.APIKey = legacyKey
		cfg.UserUUID = envloader.First("GODARK_USER_UUID", "GDX_USER_UUID")
	} else {
		apiKeyID := envloader.First("GODARK_API_KEY_ID", "GDX_API_KEY_ID")
		apiSecret := envloader.First("GODARK_API_SECRET", "GDX_API_SECRET")
		passphrase := envloader.First("GODARK_PASSPHRASE", "GDX_PASSPHRASE")
		if apiKeyID == "" || apiSecret == "" || passphrase == "" {
			log.Fatal("Missing credentials. Set GODARK_API_KEY_ID, GODARK_API_SECRET and GODARK_PASSPHRASE or legacy GODARK_API_KEY.")
		}
		cfg.APIKeyID = apiKeyID
		cfg.APISecret = apiSecret
		cfg.Passphrase = passphrase
	}

	client, err := godark.NewClient(cfg)
	if err != nil {
		log.Fatalf("WS config error: %v", err)
	}

	client.OnLeverageSettings(func(ls *godark.LeverageSettings) {
		leverageCount++
		parts := make([]string, 0, 5)
		for i, row := range ls.Settings {
			if i >= 5 {
				break
			}
			parts = append(parts, fmt.Sprintf("%d=%dx", row.SymbolID, row.Leverage))
		}
		suffix := ""
		if len(ls.Settings) > 5 {
			suffix = "..."
		}
		fmt.Printf("LEVERAGE settings=[%s%s]\n", strings.Join(parts, ", "), suffix)
	})

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

	// Leverage is per-symbol account state (not a PlaceOrder/MassQuote field).
	fmt.Println("Setting leverage to 1 via UpdateLeverage...")
	if levAck, levErr := client.UpdateLeverage(ctx, symbol, 1); levErr != nil {
		envloader.PrintOrderError("UpdateLeverage rejected", levErr)
	} else {
		fmt.Printf("UpdateLeverage: success=%v  order_id=%s\n", levAck.Success, levAck.OrderID)
	}

	// Place a limit BUY.
	mark := liveMarkPrice()
	buyPx := math.Round(mark*0.997*10) / 10
	fmt.Printf("Placing limit BUY @ %.1f (mark=%.1f)...\n", buyPx, mark)
	buyAck, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:    symbol,
		Side:      godark.SideBuy,
		OrderType: godark.OrderTypeLimit,
		Price:     buyPx,
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
	modifyPx := math.Round(mark*0.996*10) / 10
	fmt.Printf("Modifying order price to %.1f...\n", modifyPx)
	newPrice := modifyPx
	if mAck, mErr := client.ModifyOrder(ctx, buyAck.OrderID, symbol, &newPrice, nil, nil); mErr != nil {
		envloader.PrintOrderError("Modify rejected", mErr)
	} else {
		fmt.Printf("Modified: order_id=%s\n", mAck.OrderID)
	}

	time.Sleep(1 * time.Second)
	drainOrderUpdates(client, "after MODIFY")

	// Place + immediately cancel a SELL.
	sellPx := math.Round(mark*1.03*10) / 10
	fmt.Printf("Placing limit SELL @ %.1f...\n", sellPx)
	if sellAck, sErr := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:    symbol,
		Side:      godark.SideSell,
		OrderType: godark.OrderTypeLimit,
		Price:     sellPx,
		Quantity:  0.05,
		Options:   godark.PlaceOrderOptions{PostOnly: true},
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
	// Anchor the ladder/cross to the live BTC mark captured from the snapshot so
	// the crossing demo below is deterministic regardless of current price. Fall
	// back to GDX_BASE (default 64000) only if no mark was seen yet.
	base := lastBtcMark
	if base <= 0 {
		base = 64000.0
		if v, perr := strconv.ParseFloat(os.Getenv("GDX_BASE"), 64); perr == nil && v > 0 {
			base = v
		}
	}
	fmt.Printf("Mass-quoting a 3-level BUY ladder (post-only), base=%.2f...\n", base)
	ladder := []godark.MassQuoteLegInput{
		{Side: godark.SideBuy, Price: base * (1 - 0.003), Quantity: 0.02},
		{Side: godark.SideBuy, Price: base * (1 - 0.006), Quantity: 0.02},
		{Side: godark.SideBuy, Price: base * (1 - 0.009), Quantity: 0.02},
	}
	var restingIDs []uint64
	if mq, mqErr := client.MassQuote(ctx, symbol, ladder, nil); mqErr != nil {
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
		fmt.Println("cancel_all_orders (cleanup ladder)...")
		if ca, caErr := client.CancelAllOrders(ctx, symbol); caErr != nil {
			envloader.PrintOrderError("cancel_all rejected", caErr)
		} else {
			fmt.Printf("  cancel_all: count=%d ids=%v\n", ca.Count, ca.OrderIDs)
		}
		time.Sleep(500 * time.Millisecond)
		drainOrderUpdates(client, "after CANCEL ALL")
	}

	// Demonstrate the batch-level post_only flag on a crossing leg. Price a BUY
	// ~5% above the live mark: aggressive enough to cross the resting ask, yet
	// within the exchange's 10%-of-oracle limit. Anchored to the live mark, this
	// makes the post_only=true (reject) vs false (fill) contrast deterministic.
	crossPx := base * 1.05
	// post_only=true: a crossing leg is rejected (would-cross, error_code 2018).
	postOnlyTrue := true
	fmt.Println("Mass-quoting a crossing BUY with post_only=true (expect rejected/2018)...")
	if mq, mqErr := client.MassQuote(ctx, symbol,
		[]godark.MassQuoteLegInput{{Side: godark.SideBuy, Price: crossPx, Quantity: 0.001}},
		&postOnlyTrue); mqErr != nil {
		envloader.PrintOrderError("post_only=true mass quote rejected", mqErr)
	} else {
		for _, r := range mq.Results {
			errStr := "<nil>"
			if r.ErrorCode != nil {
				errStr = fmt.Sprintf("%d", *r.ErrorCode)
			}
			fmt.Printf("  leg %d: status=%s  err=%s  fills=%d\n", r.LegIndex, r.Status, errStr, r.FillCount)
		}
	}
	time.Sleep(500 * time.Millisecond)

	// post_only=false (relaxed): the crossing leg takes liquidity up to its
	// limit and rests the remainder; taker fills are reported per leg as FillCount.
	postOnlyFalse := false
	fmt.Println("Mass-quoting a crossing BUY with post_only=false (expect filled, fills>0)...")
	if mq, mqErr := client.MassQuote(ctx, symbol,
		[]godark.MassQuoteLegInput{{Side: godark.SideBuy, Price: crossPx, Quantity: 0.003}},
		&postOnlyFalse); mqErr != nil {
		envloader.PrintOrderError("post_only=false mass quote rejected", mqErr)
	} else {
		var strayIDs []uint64
		for _, r := range mq.Results {
			errStr := "<nil>"
			if r.ErrorCode != nil {
				errStr = fmt.Sprintf("%d", *r.ErrorCode)
			}
			fmt.Printf("  leg %d: status=%s  new_order_id=%s  err=%s  fills=%d\n",
				r.LegIndex, r.Status, r.NewOrderID, errStr, r.FillCount)
			if r.Status == "open" && r.NewOrderID != "" {
				if id, err := strconv.ParseUint(r.NewOrderID, 10, 64); err == nil {
					strayIDs = append(strayIDs, id)
				}
			}
		}
		if len(strayIDs) > 0 {
			fmt.Printf("Batch-cancelling %d post_only=false remainder(s)...\n", len(strayIDs))
			if bc, bcErr := client.BatchCancel(ctx, symbol, strayIDs); bcErr != nil {
				envloader.PrintOrderError("post_only=false remainder cancel rejected", bcErr)
			} else {
				for _, r := range bc.Results {
					errStr := "<nil>"
					if r.ErrorCode != nil {
						errStr = fmt.Sprintf("%d", *r.ErrorCode)
					}
					fmt.Printf("  cancel id=%s: cancelled=%v err=%s\n", r.OrderID, r.Cancelled, errStr)
				}
			}
		}
	}
	time.Sleep(1 * time.Second)
	drainOrderUpdates(client, "after post_only mass quotes")

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

	fmt.Println(sep)
	fmt.Println("  Session complete")
	fmt.Printf("  Pushes: snapshots=%d  health=%d  balance=%d  margin=%d  funding=%d  settle=%d  leverage=%d\n",
		snapCount, healthCount, balanceCount, marginCount, fundingCount, settleCount, leverageCount)
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
			badges := ""
			if u.CancelReason != "" {
				badges += fmt.Sprintf("  cancel_reason=%s", u.CancelReason)
			}
			if u.ReduceOnly {
				badges += "  reduce_only=true"
			}
			if u.PostOnly {
				badges += "  post_only=true"
			}
			fmt.Printf("ORDER  %s  id=%s  status=%s  filled=%s  remaining=%s%s\n",
				u.UpdateType, u.OrderID, u.Status, u.FilledQty, u.RemainingQty, badges)
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
				if row.SymbolID == btcSymbolID && row.MarkPrice != "" {
					if v, perr := strconv.ParseFloat(row.MarkPrice, 64); perr == nil && v > 0 {
						lastBtcMark = v
					}
				}
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
			fmt.Printf("HEALTH component=%s  state=%d  serving=%v  cause=%q\n",
				h.ComponentID, h.State, h.Serving, h.Cause)
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
			fmt.Printf("BAL    balance_raw=%d\n", b.BalanceRaw)
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
			fmt.Printf("FUND   symbol=%d  rate=%s  last=%s\n",
				f.SymbolID, f.FundingRate, f.LastFundingRate)
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
