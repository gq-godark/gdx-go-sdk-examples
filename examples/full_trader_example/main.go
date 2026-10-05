// GoDark Go SDK -- Trader Reference Example
//
// Demonstrates:
//
//  1. Load credentials from `.env` / environment.
//  2. Connect: REST access token, then WebSocket login and HPKE.
//  3. Wire up channel-first push receivers (order / position / health / etc.).
//  4. Subscribe to the private order + position channels.
//  5. Place, modify, and cancel orders. Prices and sizes are decimal strings.
//     SlippageBps is only for MARKET and STOP_MARKET. PEG is incompatible
//     with post-only. ClientOrderID is registered only after a successful
//     WebSocket place and only when POST /orders/_register_coid returns 200.
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
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk-examples/examples/internal/envloader"
)

const (
	symbol     = "BTC-USDC-PERP"
	cancelWait = 1200 * time.Millisecond
)

// btcSymbolID is BTC-USDC-PERP's numeric symbol id in position snapshots.
const btcSymbolID = 1

// lastBtcMark holds the most recent BTC mark seen in a positions snapshot.
// Zero means the snapshot had no mark; the sample then uses open interest.
var lastBtcMark float64

var leverageCount atomic.Int64
var leverageNotes = make(chan string, 32)

func main() {
	envloader.LoadDotenv()

	sep := strings.Repeat("=", 60)
	fmt.Println(sep)
	fmt.Println("  GoDark Go SDK -- Trader Reference Example")
	fmt.Println(sep)
	fmt.Println("Order types: MARKET, LIMIT, PEG, STOP_MARKET, STOP_LIMIT")
	fmt.Println("SlippageBps: MARKET and STOP_MARKET only. PEG cannot be post-only.")

	legacyKey := envloader.First("GODARK_API_KEY", "GDX_API_KEY")
	edge := envloader.First("GODARK_EDGE_URL", "GDX_EDGE_URL")
	wsURL := envloader.WSOrigin(edge)
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
		BaseURL:     envloader.WSOrigin(edge),
		Account:     envloader.First("GODARK_ACCOUNT", "GDX_ACCOUNT"),
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
		leverageCount.Add(1)
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
		note := fmt.Sprintf("LEVERAGE settings=[%s%s]", strings.Join(parts, ", "), suffix)
		select {
		case leverageNotes <- note:
		default:
		}
	})

	if err := client.Connect(ctx); err != nil {
		log.Fatalf("WS connect failed: %v", err)
	}

	fmt.Printf("WS authenticated as account=%s  (session encrypted)\n", client.Account())

	if err := client.Subscribe(ctx, "orders", "positions"); err != nil {
		_ = client.Disconnect()
		fmt.Println("Disconnected cleanly")
		log.Fatalf("Subscribe failed: %v", err)
	}
	fmt.Println("Subscribed to order + position updates")

	exitCode := 0
	pending := map[string]time.Time{}
	defer func() {
		for id, at := range pending {
			if wait := cancelWait - time.Since(at); wait > 0 {
				time.Sleep(wait)
			}
			if _, cerr := client.CancelOrder(ctx, id, symbol); cerr != nil {
				envloader.PrintOrderError("cancel "+id, cerr)
				exitCode = 1
			} else {
				fmt.Printf("cancel OK -- order_id=%s\n", id)
				delete(pending, id)
			}
		}
		_ = client.Disconnect()
		fmt.Println("Disconnected cleanly")
		if exitCode != 0 {
			os.Exit(exitCode)
		}
	}()

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

	restCfg := godark.RestClientConfig{
		BaseURL:    envloader.HTTPOrigin(edge),
		Account:    cfg.Account,
		APIKey:     cfg.APIKey,
		APIKeyID:   cfg.APIKeyID,
		APISecret:  cfg.APISecret,
		Passphrase: cfg.Passphrase,
	}
	var oi []map[string]any
	if rest, restErr := godark.NewRestClient(restCfg); restErr != nil {
		fmt.Fprintf(os.Stderr, "REST client: %v\n", restErr)
		exitCode = 1
		return
	} else if rows, oiErr := rest.GetOpenInterest(ctx); oiErr == nil {
		oi = rows
	}
	mark, markErr := envloader.ResolveMark(lastBtcMark, oi)
	if markErr != nil {
		fmt.Fprintf(os.Stderr, "live mark: %v\n", markErr)
		exitCode = 1
		return
	}
	fmt.Printf("live mark=%.4f\n", mark)

	buyPx, err := envloader.PostOnlyBuy(mark, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "buy price: %v\n", err)
		exitCode = 1
		return
	}
	fmt.Printf("Placing post-only limit BUY @ %s qty=%s...\n", buyPx, envloader.DemoQuantity)
	buyAck, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:    symbol,
		Side:      godark.SideBuy,
		OrderType: godark.OrderTypeLimit,
		Price:     buyPx,
		Quantity:  envloader.DemoQuantity,
		Options:   godark.PlaceOrderOptions{PostOnly: true},
	})
	if err != nil {
		envloader.PrintOrderError("BUY rejected", err)
		exitCode = 1
		return
	}
	fmt.Printf("BUY placed: order_id=%s  sequence=%s\n", buyAck.OrderID, buyAck.Sequence)
	buyAt := time.Now()
	pending[buyAck.OrderID] = buyAt

	if wait := cancelWait - time.Since(buyAt); wait > 0 {
		time.Sleep(wait)
	}
	drainOrderUpdates(client, "after BUY")

	modifyPx, err := envloader.PostOnlyBuy(mark, 1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modify price: %v\n", err)
		exitCode = 1
		return
	}
	fmt.Printf("Modifying order price to %s...\n", modifyPx)
	if mAck, mErr := client.ModifyOrder(ctx, buyAck.OrderID, symbol, &modifyPx, nil, nil); mErr != nil {
		envloader.PrintOrderError("Modify rejected", mErr)
		exitCode = 1
		return
	} else {
		fmt.Printf("Modified: order_id=%s\n", mAck.OrderID)
	}
	time.Sleep(cancelWait)
	drainOrderUpdates(client, "after MODIFY")

	// A market IOC can fill and leave a position. This sample does not send one.
	fmt.Println("Skipping market IOC so the sample does not open a position.")

	sellPx, err := envloader.PostOnlySell(mark)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sell price: %v\n", err)
		exitCode = 1
		return
	}
	fmt.Printf("Placing post-only limit SELL @ %s qty=%s...\n", sellPx, envloader.DemoQuantity)
	sellAck, sErr := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:    symbol,
		Side:      godark.SideSell,
		OrderType: godark.OrderTypeLimit,
		Price:     sellPx,
		Quantity:  envloader.DemoQuantity,
		Options:   godark.PlaceOrderOptions{PostOnly: true},
	})
	if sErr != nil {
		envloader.PrintOrderError("SELL rejected", sErr)
		exitCode = 1
		return
	}
	fmt.Printf("SELL placed: order_id=%s\n", sellAck.OrderID)
	sellAt := time.Now()
	pending[sellAck.OrderID] = sellAt
	if wait := cancelWait - time.Since(sellAt); wait > 0 {
		time.Sleep(wait)
	}
	if cAck, cErr := client.CancelOrder(ctx, sellAck.OrderID, symbol); cErr != nil {
		envloader.PrintOrderError("Cancel SELL rejected", cErr)
		exitCode = 1
		return
	} else {
		fmt.Printf("SELL cancelled: order_id=%s\n", cAck.OrderID)
		delete(pending, sellAck.OrderID)
	}

	drainOrderUpdates(client, "after SELL/CANCEL")

	// Post-only ladder. Every leg is at least 500 below the live mark and is
	// cancelled by id before the process exits.
	postOnly := true
	offsets := []float64{0, 10, 20}
	ladder := make([]godark.MassQuoteLegInput, 0, len(offsets))
	for _, extra := range offsets {
		px, pxErr := envloader.PostOnlyBuy(mark, extra)
		if pxErr != nil {
			fmt.Fprintf(os.Stderr, "ladder price: %v\n", pxErr)
			exitCode = 1
			return
		}
		ladder = append(ladder, godark.MassQuoteLegInput{
			Side: godark.SideBuy, Price: px, Quantity: envloader.DemoQuantity,
		})
	}
	fmt.Printf("Mass-quoting a %d-level post-only BUY ladder (mark=%.4f)...\n", len(ladder), mark)
	ladderAt := time.Now()
	var restingIDs []uint64
	if mq, mqErr := client.MassQuote(ctx, symbol, ladder, &postOnly); mqErr != nil {
		envloader.PrintOrderError("Mass quote rejected", mqErr)
		exitCode = 1
		return
	} else {
		fmt.Printf("Mass quote: success=%v  sequence=%s  legs=%d\n", mq.Success, mq.Sequence, len(mq.Results))
		for _, r := range mq.Results {
			errStr := "<nil>"
			if r.ErrorCode != nil {
				errStr = fmt.Sprintf("%d", *r.ErrorCode)
			}
			fmt.Printf("  leg %d: status=%s  new_order_id=%s  fills=%d  err=%s\n",
				r.LegIndex, r.Status, r.NewOrderID, r.FillCount, errStr)
			if r.NewOrderID != "" {
				pending[r.NewOrderID] = ladderAt
				if id, perr := strconv.ParseUint(r.NewOrderID, 10, 64); perr == nil {
					restingIDs = append(restingIDs, id)
				}
			}
			if r.Status == "filled" || r.FillCount > 0 {
				fmt.Fprintf(os.Stderr, "ladder leg filled; will cancel and exit non-zero\n")
				exitCode = 1
			}
		}
	}

	if wait := cancelWait - time.Since(ladderAt); wait > 0 {
		time.Sleep(wait)
	}
	drainOrderUpdates(client, "after MASS QUOTE")

	if len(restingIDs) > 0 {
		fmt.Printf("Cancelling %d ladder order(s) by id...\n", len(restingIDs))
		for _, id := range restingIDs {
			oid := strconv.FormatUint(id, 10)
			if ca, caErr := client.CancelOrder(ctx, oid, symbol); caErr != nil {
				envloader.PrintOrderError("cancel "+oid+" rejected", caErr)
				exitCode = 1
			} else {
				fmt.Printf("  cancel order_id=%s\n", ca.OrderID)
				delete(pending, oid)
			}
		}
		drainOrderUpdates(client, "after CANCEL ALL")
	} else {
		fmt.Fprintln(os.Stderr, "mass quote opened no orders")
		exitCode = 1
		return
	}

	// Second post-only quote, further below the mark, cancelled with BatchCancel.
	restPx, err := envloader.PostOnlyBuy(mark, 40)
	if err != nil {
		fmt.Fprintf(os.Stderr, "quote price: %v\n", err)
		exitCode = 1
		return
	}
	fmt.Printf("Mass-quoting a post-only BUY @ %s (batch-cancelled)...\n", restPx)
	quoteAt := time.Now()
	if mq, mqErr := client.MassQuote(ctx, symbol,
		[]godark.MassQuoteLegInput{{Side: godark.SideBuy, Price: restPx, Quantity: envloader.DemoQuantity}},
		&postOnly); mqErr != nil {
		envloader.PrintOrderError("post-only mass quote rejected", mqErr)
		exitCode = 1
		return
	} else {
		var strayIDs []uint64
		for _, r := range mq.Results {
			errStr := "<nil>"
			if r.ErrorCode != nil {
				errStr = fmt.Sprintf("%d", *r.ErrorCode)
			}
			fmt.Printf("  leg %d: status=%s  new_order_id=%s  err=%s  fills=%d\n",
				r.LegIndex, r.Status, r.NewOrderID, errStr, r.FillCount)
			if r.NewOrderID != "" {
				pending[r.NewOrderID] = quoteAt
				if id, idErr := strconv.ParseUint(r.NewOrderID, 10, 64); idErr == nil {
					strayIDs = append(strayIDs, id)
				}
			}
			if r.Status == "filled" || r.FillCount > 0 {
				exitCode = 1
			}
		}
		if len(strayIDs) == 0 {
			fmt.Fprintln(os.Stderr, "post-only quote opened no orders")
			exitCode = 1
			return
		}
		if wait := cancelWait - time.Since(quoteAt); wait > 0 {
			time.Sleep(wait)
		}
		fmt.Printf("Batch-cancelling %d post-only quote(s)...\n", len(strayIDs))
		if bc, bcErr := client.BatchCancel(ctx, symbol, strayIDs); bcErr != nil {
			envloader.PrintOrderError("quote batch cancel rejected", bcErr)
			exitCode = 1
		} else {
			for _, r := range bc.Results {
				errStr := "<nil>"
				if r.ErrorCode != nil {
					errStr = fmt.Sprintf("%d", *r.ErrorCode)
				}
				fmt.Printf("  cancel id=%s: cancelled=%v err=%s\n", r.OrderID, r.Cancelled, errStr)
				if r.Cancelled {
					delete(pending, r.OrderID)
				} else {
					exitCode = 1
				}
			}
		}
	}
	drainOrderUpdates(client, "after post_only mass quotes")

	fmt.Println("Cancelling original BUY (cleanup)...")
	if wait := cancelWait - time.Since(buyAt); wait > 0 {
		time.Sleep(wait)
	}
	if _, err := client.CancelOrder(ctx, buyAck.OrderID, symbol); err != nil {
		envloader.PrintOrderError("Cancel BUY", err)
		exitCode = 1
	} else {
		fmt.Println("Original BUY cancelled")
		delete(pending, buyAck.OrderID)
	}

	// Drain anything that arrived during the session.
	snapCount := drainPositionsSnapshots(client)
	healthCount := drainHealth(client)
	balanceCount := drainBalances(client)
	marginCount := drainMargins(client)
	fundingCount := drainFunding(client)
	settleCount := drainSettlement(client)

	for {
		select {
		case note := <-leverageNotes:
			fmt.Println(note)
		default:
			goto leverageDrained
		}
	}
leverageDrained:
	fmt.Println(sep)
	fmt.Println("  Session complete")
	fmt.Printf("  Pushes: snapshots=%d  health=%d  balance=%d  margin=%d  funding=%d  settle=%d  leverage=%d\n",
		snapCount, healthCount, balanceCount, marginCount, fundingCount, settleCount, leverageCount.Load())
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
