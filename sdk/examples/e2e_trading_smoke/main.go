// GoDark Go SDK -- End-to-end trading smoke test
//
// Mirrors the python `check_edge_api_keys.py`, cpp `e2e_trading_smoke`, and
// rust `e2e_trading_smoke.rs`: connect -> ECDH -> (optional) place + cancel.
//
// Environment (GODARK_* preferred; GDX_* aliases for parity):
//   GODARK_API_KEY_ID / GDX_API_KEY_ID
//   GODARK_API_SECRET / GDX_API_SECRET
//   GODARK_EDGE_URL / GDX_EDGE_URL (optional)
//
// Run with:
//
//	go run ./examples/e2e_trading_smoke
//	go run ./examples/e2e_trading_smoke -- --auth-only
//
// Exit codes:
//   0 success    1 config       2 connect/auth/session
//   3 place failed              4 cancel failed
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk/examples/internal/envloader"
)

func main() {
	envloader.LoadDotenv()

	authOnly, err := parseArgs()
	if err != nil {
		printUsage()
		os.Exit(1)
	}

	apiKeyID, apiSecret, err := credentials()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	t0 := time.Now()

	client, err := godark.NewClient(godark.ClientConfig{
		APIKeyID:  apiKeyID,
		APISecret: apiSecret,
	})
	if err != nil {
		mapEarlyError(err)
	}

	fmt.Fprintln(os.Stderr, "[e2e] Connecting (GODARK_EDGE_URL / GDX_EDGE_URL or default) ...")
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		mapEarlyError(err)
	}

	connectMS := time.Since(t0).Milliseconds()
	fmt.Fprintf(os.Stderr, "[e2e] Auth + ECDH OK -- user_uuid=%s (%d ms)\n", client.UserUUID(), connectMS)

	if authOnly {
		_ = client.Disconnect()
		fmt.Fprintln(os.Stderr, "[e2e] --auth-only: skipping orders. Done.")
		return
	}

	if err := client.Subscribe(ctx, "orders", "positions"); err != nil {
		_ = client.Disconnect()
		mapEarlyError(err)
	}

	const (
		symbol = "BTC-USDC-PERP"
		qty    = 0.01
		price  = 999_999.0
	)
	fmt.Fprintf(os.Stderr, "[e2e] Placing LIMIT SELL %v @ %v ...\n", qty, price)

	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol: symbol, Side: godark.SideSell, OrderType: godark.OrderTypeLimit,
		Quantity: qty, Price: price,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[e2e] place_order: %v\n", err)
		_ = client.Disconnect()
		os.Exit(3)
	}
	fmt.Fprintf(os.Stderr, "[e2e] Place OK -- order_id=%s sequence=%s\n", ack.OrderID, ack.Sequence)

	fmt.Fprintln(os.Stderr, "[e2e] Cancelling order ...")
	if _, err := client.CancelOrder(ctx, ack.OrderID, symbol); err != nil {
		fmt.Fprintf(os.Stderr, "[e2e] cancel_order: %v\n", err)
		_ = client.Disconnect()
		os.Exit(4)
	}
	fmt.Fprintf(os.Stderr, "[e2e] Cancel OK -- order_id=%s\n", ack.OrderID)

	_ = client.Disconnect()
	fmt.Fprintf(os.Stderr, "[e2e] Full encrypted trading path validated (%d ms total).\n",
		time.Since(t0).Milliseconds())
}

func parseArgs() (authOnly bool, err error) {
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--help", "-h":
			printUsage()
			os.Exit(0)
		case "--auth-only":
			authOnly = true
		default:
			return false, fmt.Errorf("unknown argument: %s", arg)
		}
	}
	return authOnly, nil
}

func printUsage() {
	fmt.Fprintln(os.Stderr,
		"e2e_trading_smoke -- GoDark Go SDK end-to-end check\n\n"+
			"Environment:\n"+
			"  GODARK_API_KEY_ID / GDX_API_KEY_ID\n"+
			"  GODARK_API_SECRET / GDX_API_SECRET\n"+
			"  GODARK_EDGE_URL / GDX_EDGE_URL (optional)\n\n"+
			"Options:\n"+
			"  --auth-only     Connect + ECDH only (no orders)\n"+
			"  --help          Show this message")
}

func envFirst(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func credentials() (string, string, error) {
	id := envFirst("GODARK_API_KEY_ID", "GDX_API_KEY_ID")
	secret := envFirst("GODARK_API_SECRET", "GDX_API_SECRET")
	if id == "" || secret == "" {
		return "", "", errors.New(
			"missing credentials. Set GODARK_API_KEY_ID and GODARK_API_SECRET (or GDX_* aliases).")
	}
	return id, secret, nil
}

// mapEarlyError maps an SDK error to one of the connect-time exit codes (2
// for auth/connect/session/encryption/timeout, 3 for order errors that
// surface during the connect path) and never returns.
func mapEarlyError(err error) {
	fmt.Fprintf(os.Stderr, "[e2e] Error: %v\n", err)
	var oe *godark.OrderError
	if errors.As(err, &oe) {
		os.Exit(3)
	}
	os.Exit(2)
}
