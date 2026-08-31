package godark

import (
	"testing"

	commonpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/common/v1"
	sequencerpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/sequencer/v1"
	"google.golang.org/protobuf/proto"
)

func TestCancelReasonProto6289(t *testing.T) {
	cases := map[int32]CancelReason{
		6: CancelReasonADL,
		7: CancelReasonLiquidatedCanceled,
		8: CancelReasonMarginCanceled,
		9: CancelReasonReduceOnly,
		10: CancelReasonStpExpireTaker,
		11: CancelReasonStpCancelResting,
	}
	for wire, want := range cases {
		got, ok := cancelReasonFromProto[wire]
		if !ok {
			t.Fatalf("cancelReasonFromProto missing %d", wire)
		}
		if got != want {
			t.Errorf("cancelReasonFromProto[%d] = %q, want %q", wire, got, want)
		}
	}
}

func TestOrderUpdateReduceOnlyPostOnly(t *testing.T) {
	cr := commonpb.CancelReason_CANCEL_REASON_REDUCE_ONLY
	msg := &sequencerpb.OrderUpdateMessage{
		MessageType: commonpb.OrderUpdateType_ORDER_UPDATE_TYPE_CANCELLED,
		OrderId:     42,
		UserUuid:    make([]byte, 16),
		SymbolId:    1,
		OrderStatus: commonpb.OrderStatus_ORDER_STATUS_CANCELLED,
		Price:       "1",
		Quantity:    "1",
		Side:        commonpb.Side_SIDE_BUY,
		FilledQty:   "0",
		RemainingQty: "1",
		CumFill:     "0",
		CancelReason: &cr,
		ReduceOnly:  true,
		PostOnly:    false,
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	u, err := ParseOrderUpdate(data)
	if err != nil {
		t.Fatal(err)
	}
	if !u.ReduceOnly {
		t.Fatal("expected ReduceOnly true")
	}
	if u.PostOnly {
		t.Fatal("expected PostOnly false")
	}
	if u.CancelReason != CancelReasonReduceOnly {
		t.Fatalf("cancel reason = %q, want REDUCE_ONLY", u.CancelReason)
	}
}

func TestResponseMessageTypeToProto(t *testing.T) {
	cases := map[string]int32{
		"tpsl_update":       11,
		"leverage_settings": 12,
		"tpsl_ack":          16,
	}
	for messageType, want := range cases {
		got, ok := responseMessageTypeToProto[messageType]
		if !ok {
			t.Fatalf("responseMessageTypeToProto missing %q", messageType)
		}
		if got != want {
			t.Errorf("responseMessageTypeToProto[%q] = %d, want %d", messageType, got, want)
		}
	}
}
