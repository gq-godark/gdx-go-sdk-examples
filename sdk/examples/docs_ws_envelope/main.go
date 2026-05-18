// GoDark Go SDK -- Raw WebSocket docs-envelope example
//
// Demonstrates the docs-wire envelope by hand (without using GodarkClient),
// to document the on-wire layout: login -> ping/pong -> subscribe -> logout.
//
// Public docs base URLs:
//
//	Mainnet WS:  wss://api.godarkdex.com/ws/v1
//	Testnet WS:  wss://api.godarkdex-testnet.com/ws/v1
//
// For local testing this example defaults to ws://127.0.0.1:4000/ws/v1.
//
// Auth:
//
//	Real credentials: GODARK_API_KEY_ID + GODARK_API_SECRET
//	  -> sends `login.args.token = "<key_id>:<secret>"`
//	Local static-key edge: GODARK_AUTH_TOKEN=test-key-1
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/gq-godark/gdx-go-sdk/examples/internal/envloader"
)

func main() {
	envloader.LoadDotenv()

	wsURL := envFirst("GODARK_WS_URL", "GDX_WS_URL")
	if wsURL == "" {
		wsURL = "ws://127.0.0.1:4000/ws/v1"
	}
	token, err := authToken()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Connecting to %s\n", wsURL)

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDial()
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	if err := sendAndExpect(conn, frame("login", map[string]any{"token": token}), "login"); err != nil {
		log.Fatal(err)
	}
	if err := sendAndExpect(conn, frame("ping", map[string]any{}), "pong"); err != nil {
		log.Fatal(err)
	}

	sub := frame("subscribe", []map[string]any{
		{"channel": "orders"},
		{"channel": "positions"},
	})
	if err := sendAndExpect(conn, sub, "subscribe"); err != nil {
		log.Fatal(err)
	}
	if err := recvAndExpect(conn, sub["id"].(string), "subscribe"); err != nil {
		log.Fatal(err)
	}

	if err := sendAndExpect(conn, frame("logout", map[string]any{}), "logout"); err != nil {
		log.Fatal(err)
	}

	fmt.Println("OK: docs-envelope login, ping, subscribe, and logout succeeded")
}

func envFirst(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func authToken() (string, error) {
	if v := envFirst("GODARK_AUTH_TOKEN", "GDX_AUTH_TOKEN"); v != "" {
		return v, nil
	}
	keyID := envFirst("GODARK_API_KEY_ID", "GDX_API_KEY_ID", "GODARK_API_KEY", "GDX_API_KEY")
	secret := envFirst("GODARK_API_SECRET", "GDX_API_SECRET")
	if keyID == "" || secret == "" {
		return "", fmt.Errorf("set GODARK_API_KEY_ID + GODARK_API_SECRET, or for localnet set GODARK_AUTH_TOKEN=test-key-1")
	}
	return keyID + ":" + secret, nil
}

func frame(op string, args any) map[string]any {
	return map[string]any{
		"id":   uuid.NewString(),
		"op":   op,
		"args": args,
	}
}

func sendAndExpect(conn *websocket.Conn, payload map[string]any, expectedOp string) error {
	fmt.Printf("SEND %s\n", printable(payload))
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, buf); err != nil {
		return err
	}
	id, _ := payload["id"].(string)
	return recvAndExpect(conn, id, expectedOp)
}

func recvAndExpect(conn *websocket.Conn, expectedID, expectedOp string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("socket closed waiting for response: %w", err)
	}
	fmt.Printf("RECV %s\n", string(data))
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		return err
	}
	if expectedID != "" {
		if gotID, _ := msg["id"].(string); gotID != expectedID {
			return fmt.Errorf("expected id %s, got %v", expectedID, msg["id"])
		}
	}
	if gotOp, _ := msg["op"].(string); gotOp != expectedOp {
		return fmt.Errorf("expected op %s, got %v", expectedOp, msg["op"])
	}
	if code, ok := msg["code"].(float64); !ok || code != 0 {
		return fmt.Errorf("expected code 0, got %v: %v", msg["code"], msg["message"])
	}
	return nil
}

func printable(payload map[string]any) string {
	if op, _ := payload["op"].(string); op != "login" {
		buf, _ := json.Marshal(payload)
		return string(buf)
	}
	clone := map[string]any{}
	for k, v := range payload {
		clone[k] = v
	}
	if args, ok := clone["args"].(map[string]any); ok {
		argsClone := map[string]any{}
		for k, v := range args {
			argsClone[k] = v
		}
		argsClone["token"] = "<redacted>"
		clone["args"] = argsClone
	}
	buf, _ := json.Marshal(clone)
	return string(buf)
}
