package godark

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPlaceOutcomeWaiterHandlesPushBeforeAck(t *testing.T) {
	client := &GodarkClient{placeTerminalTimeout: time.Second}
	waiter := client.registerPlaceOutcomeWaiter()
	update := &OrderUpdate{
		OrderID:    "42",
		Status:     OrderStatusNew,
		UpdateType: OrderUpdateTypeOpen,
	}

	client.observeOrderUpdate(update)
	got, err := client.awaitPlaceOutcome(context.Background(), "42", waiter)
	if err != nil {
		t.Fatalf("awaitPlaceOutcome: %v", err)
	}
	if got != update {
		t.Fatalf("got %#v, want %#v", got, update)
	}
}

func TestTerminalPlaceUpdateIncludesRejectedStatus(t *testing.T) {
	if !isTerminalPlaceUpdate(&OrderUpdate{Status: OrderStatusRejected}) {
		t.Fatal("REJECTED status must settle a place waiter")
	}
	for _, ut := range []OrderUpdateType{
		OrderUpdateTypeOpen,
		OrderUpdateTypeRejected,
		OrderUpdateTypeFilled,
		OrderUpdateTypePartiallyFilled,
		OrderUpdateTypeCancelled,
	} {
		if !isTerminalPlaceUpdate(&OrderUpdate{UpdateType: ut}) {
			t.Fatalf("%s must settle a place waiter", ut)
		}
	}
}

func TestPlaceOutcomeBookRejectedMapsOrderError(t *testing.T) {
	client := &GodarkClient{placeTerminalTimeout: time.Second}
	waiter := client.registerPlaceOutcomeWaiter()
	update := &OrderUpdate{
		OrderID:      "99",
		Status:       OrderStatusRejected,
		UpdateType:   OrderUpdateTypeRejected,
		RejectReason: "2007",
		Msg:          "price too far from mark",
	}
	client.observeOrderUpdate(update)

	got, err := client.awaitPlaceOutcome(context.Background(), "99", waiter)
	if err != nil {
		t.Fatalf("awaitPlaceOutcome: %v", err)
	}
	if got.UpdateType != OrderUpdateTypeRejected {
		t.Fatalf("update type: got %s", got.UpdateType)
	}

	code := int32(2007)
	oe := MakeOrderErrorFromCode(&code, got.Msg)
	if oe.ErrorCode != "PRICE_DEVIATION_TOO_LARGE" {
		t.Fatalf("ErrorCode: got %q", oe.ErrorCode)
	}
	if !strings.Contains(oe.Message, "PRICE_DEVIATION_TOO_LARGE") ||
		!strings.Contains(oe.Message, "price too far from mark") {
		t.Fatalf("Message: %q", oe.Message)
	}
}

func TestPlaceOutcomeTimeout(t *testing.T) {
	client := &GodarkClient{placeTerminalTimeout: 20 * time.Millisecond}
	waiter := client.registerPlaceOutcomeWaiter()
	start := time.Now()
	_, err := client.awaitPlaceOutcome(context.Background(), "1", waiter)
	if err == nil {
		t.Fatal("expected timeout")
	}
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want TimeoutError, got %T: %v", err, err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

func TestPlaceOutcomeDisconnectRejectsWaiters(t *testing.T) {
	client := &GodarkClient{placeTerminalTimeout: time.Second}
	client.recentTerminalOrders = []*OrderUpdate{{OrderID: "stale"}}
	waiter := client.registerPlaceOutcomeWaiter()
	done := make(chan error, 1)
	go func() {
		_, err := client.awaitPlaceOutcome(context.Background(), "7", waiter)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	client.rejectPlaceOutcomeWaiters(newConnectionError(
		"connection lost while waiting for order confirmation",
	))
	err := <-done
	var ce *ConnectionError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConnectionError, got %T: %v", err, err)
	}
	client.placeMu.Lock()
	defer client.placeMu.Unlock()
	if len(client.placeOutcomeWaiters) != 0 {
		t.Fatalf("waiters not cleared: %d", len(client.placeOutcomeWaiters))
	}
	if len(client.recentTerminalOrders) != 0 {
		t.Fatalf("recent cache not cleared: %d", len(client.recentTerminalOrders))
	}
}

func TestPlaceOrderConfirmationDefaultsToBook(t *testing.T) {
	req := PlaceOrderRequest{}
	confirmation := req.Confirmation
	if confirmation == "" {
		confirmation = PlaceOrderConfirmationBook
	}
	if confirmation != PlaceOrderConfirmationBook {
		t.Fatalf("default confirmation: got %q", confirmation)
	}
	if PlaceOrderConfirmationAck != "ack" || PlaceOrderConfirmationBook != "book" {
		t.Fatal("confirmation constants must match JS ack/book strings")
	}
}

func TestMakeOrderErrorFromCodePreservesRejectText(t *testing.T) {
	code := int32(2007)
	got := MakeOrderErrorFromCode(&code, "far from mark")
	if got.ErrorCode != "PRICE_DEVIATION_TOO_LARGE" {
		t.Fatalf("ErrorCode: got %q", got.ErrorCode)
	}
	if !strings.Contains(got.Message, "PRICE_DEVIATION_TOO_LARGE") ||
		!strings.Contains(got.Message, "far from mark") {
		t.Fatalf("Message: %q", got.Message)
	}
}
