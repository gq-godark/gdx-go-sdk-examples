// GoDark Go SDK -- Market data streaming example
//
// Stream L2 orderbook + trades for BTC-USDC-PERP via the public gomarket
// WebSocket. No authentication required.
//
// Set GODARK_EDGE_URL (or GDX_EDGE_URL) to override the default edge base
// URL. The client appends `/ws/gomarket` at connect time.
//
// Run with:
//
//	go run ./examples/market_data
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk/examples/internal/envloader"
)

const (
	defaultEdgeURL = "wss://api.godark-dex.com"
	symbol         = "BTC-USDC-PERP"
	streamFor      = 30 * time.Second
)

func main() {
	envloader.LoadDotenv()

	baseURL := os.Getenv("GODARK_EDGE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("GDX_EDGE_URL")
	}
	if baseURL == "" {
		baseURL = defaultEdgeURL
	}

	client := godark.NewMarketDataClient(godark.MarketDataConfig{
		BaseURL: baseURL,
	})

	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect()

	if err := client.SubscribeOrderbook(ctx, symbol, nil); err != nil {
		log.Fatal(err)
	}
	if err := client.SubscribeTrades(ctx, symbol, nil); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Streaming market data for %s (orderbook + trades on %s)...\n", streamFor, symbol)

	orderbook := client.OrderbookEvents()
	trades := client.TradesEvents()
	deadline := time.After(streamFor)

	for {
		select {
		case <-deadline:
			fmt.Println("Stream window elapsed; disconnecting.")
			return
		case msg, ok := <-orderbook:
			if !ok {
				return
			}
			printOrderbook(msg)
		case msg, ok := <-trades:
			if !ok {
				return
			}
			printTrade(msg)
		}
	}
}

func printOrderbook(msg godark.MarketDataMessage) {
	bids, _ := msg.Raw["bids"].([]any)
	asks, _ := msg.Raw["asks"].([]any)
	bestBid := "n/a"
	if len(bids) > 0 {
		bestBid = strings.TrimSpace(fmt.Sprint(bids[0]))
	}
	bestAsk := "n/a"
	if len(asks) > 0 {
		bestAsk = strings.TrimSpace(fmt.Sprint(asks[0]))
	}
	fmt.Printf("Orderbook | best bid: %s | best ask: %s\n", bestBid, bestAsk)
}

func printTrade(msg godark.MarketDataMessage) {
	price := strFromRaw(msg.Raw, "price")
	size := strFromRaw(msg.Raw, "size")
	side := strFromRaw(msg.Raw, "side")
	fmt.Printf("Trade | price=%s size=%s side=%s\n", price, size, side)
}

func strFromRaw(p map[string]any, key string) string {
	v, ok := p[key]
	if !ok {
		return "n/a"
	}
	return fmt.Sprint(v)
}
