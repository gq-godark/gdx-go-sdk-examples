// REST-only trader demo — auth + encrypted snapshots + place/modify/cancel.
//
//	GODARK_REST_URL=https://api.devnet.godark-dex.com \
//	GODARK_API_KEY_ID=... GODARK_API_SECRET=... GODARK_PASSPHRASE=... \
//	GDX_LIVE_PRICE=78000 \
//	  go run ./examples/full_trader_rest
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk-examples/examples/internal/envloader"
)

func livePrice() float64 {
	for _, key := range []string{"GDX_LIVE_PRICE", "GODARK_LIVE_PRICE"} {
		if v := os.Getenv(key); v != "" {
			if p, err := strconv.ParseFloat(v, 64); err == nil {
				return p
			}
		}
	}
	return 78000.0
}

func main() {
	envloader.LoadDotenv()

	base := os.Getenv("GODARK_REST_URL")
	if base == "" {
		base = os.Getenv("GDX_REST_URL")
	}
	if base == "" {
		base = "https://api.godark-dex.com"
	}

	keyID := os.Getenv("GODARK_API_KEY_ID")
	if keyID == "" {
		keyID = os.Getenv("GDX_API_KEY_ID")
	}
	secret := os.Getenv("GODARK_API_SECRET")
	if secret == "" {
		secret = os.Getenv("GDX_API_SECRET")
	}
	passphrase := os.Getenv("GODARK_PASSPHRASE")
	if passphrase == "" {
		passphrase = os.Getenv("GDX_PASSPHRASE")
	}
	legacyKey := os.Getenv("GODARK_API_KEY")
	if legacyKey == "" {
		legacyKey = os.Getenv("GDX_API_KEY")
	}

	cfg := godark.RestClientConfig{BaseURL: base}
	if keyID != "" && secret != "" {
		cfg.APIKeyID = keyID
		cfg.APISecret = secret
		cfg.Passphrase = passphrase
	} else if legacyKey != "" {
		cfg.APIKey = legacyKey
	} else {
		log.Fatal("Set GODARK_API_KEY_ID, GODARK_API_SECRET and GODARK_PASSPHRASE in .env")
	}

	client, err := godark.NewRestClient(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Disconnect(ctx) }()

	fmt.Printf("identity user_uuid=%s scope=%s\n", client.UserUUID(), client.TokenScope())

	open, err := client.GetOpenOrders(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("open_orders", len(open.Rows))

	pos, err := client.GetPositions(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("positions", len(pos.Rows))

	acct, err := client.GetAccount(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if acct.Account != nil {
		fmt.Println("account total_collateral=", acct.Account.TotalCollateral)
	}

	price := livePrice()
	limitPrice := price - 5000
	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRestRequest{
		PlaceOrderRequest: godark.PlaceOrderRequest{
			Symbol: "BTC-USDC-PERP", Side: "BUY", OrderType: "LIMIT",
			Quantity: 0.01, Price: limitPrice,
		},
		ClientOrderID: "sdk-go-rest-demo",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("placed order_id=", ack.OrderID, "success=", ack.Success)

	time.Sleep(500 * time.Millisecond)

	newPrice := limitPrice - 64
	mod, err := client.ModifyOrder(ctx, ack.OrderID, "BTC-USDC-PERP", &newPrice, nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("modified success=", mod.Success)

	can, err := client.CancelOrder(ctx, ack.OrderID, "BTC-USDC-PERP")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("cancelled success=", can.Success)
}
