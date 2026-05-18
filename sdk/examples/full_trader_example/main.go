// GoDark Go SDK -- Trader Reference Example
//
// Demonstrates:
//
//  1. Load credentials from `.env` / environment.
//  2. Connect and authenticate (encrypted ECDH session).
//  3. Wire up channel-first push receivers (order / position / health / etc.).
//  4. Subscribe to the order + position channels.
//  5. Place, modify, and cancel `MARKET` / `LIMIT` orders.
//  6. Drain queued updates between actions.
//  7. Print a session summary including per-stream counts.
//  8. Clean disconnect.
//
// Run with:
//
//	go run ./examples/full_trader_example
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk/examples/internal/envloader"
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
	if apiKeyID == "" || apiSecret == "" {
		log.Fatal("Missing credentials. Set GODARK_API_KEY_ID and GODARK_API_SECRET (or provide them in .env).")
	}
	baseURL := os.Getenv("GODARK_EDGE_URL")
	if baseURL == "" {
		baseURL = "wss://api.godark-dex.com"
	}
	fmt.Printf("Endpoint: %s\n", baseURL)

	headers := http.Header{}
	headers.Set("X-Trader-Tag", "go-full-trader-demo")

	client, err := godark.NewClient(godark.ClientConfig{
		APIKeyID:  apiKeyID,
		APISecret: apiSecret,
		BaseURL:   baseURL,
		Transport: godark.TransportConfig{
			Headers:           headers,
			HeartbeatInterval: 30 * time.Second,
			StaleTimeout:      60 * time.Second,
			CommandTimeout:    10 * time.Second,
		},
	})
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer func() {
		_ = client.Disconnect()
		fmt.Println("Disconnected cleanly")
	}()

	fmt.Printf("Authenticated as user_uuid=%s  (session encrypted)\n", client.UserUUID())

	if err := client.Subscribe(ctx, "orders", "positions"); err != nil {
		log.Fatalf("Subscribe failed: %v", err)
	}
	fmt.Println("Subscribed to order + position updates")

	time.Sleep(200 * time.Millisecond)
	drainPositionUpdates(client)
	drainPositionsSnapshots(client)

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

	fmt.Println("Modifying order price to 68000...")
	newPrice := 68_000.0
	if mAck, mErr := client.ModifyOrder(ctx, buyAck.OrderID, symbol, &newPrice, nil); mErr != nil {
		envloader.PrintOrderError("Modify rejected", mErr)
	} else {
		fmt.Printf("Modified: order_id=%s\n", mAck.OrderID)
	}

	time.Sleep(1 * time.Second)
	drainOrderUpdates(client, "after MODIFY")

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

	fmt.Println("Cancelling original BUY (cleanup)...")
	if _, err := client.CancelOrder(ctx, buyAck.OrderID, symbol); err != nil {
		fmt.Println("Original BUY already filled or cancelled")
	} else {
		fmt.Println("Original BUY cancelled")
	}

	snapCount := drainPositionsSnapshots(client)
	healthCount := drainHealth(client)
	balanceCount := drainBalances(client)
	marginCount := drainMargins(client)
	fundingCount := drainFunding(client)
	settleCount := drainSettlement(client)

	fmt.Println(sep)
	fmt.Println("  Session complete")
	fmt.Printf("  Pushes: snapshots=%d  health=%d  balance=%d  margin=%d  funding=%d  settle=%d\n",
		snapCount, healthCount, balanceCount, marginCount, fundingCount, settleCount)
	fmt.Println(sep)
}

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
