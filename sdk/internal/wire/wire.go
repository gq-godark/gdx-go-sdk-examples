// Package wire encodes and decodes TradingWsBinaryFrame protobuf frames.
package wire

import (
	"encoding/base64"
	"encoding/hex"

	"google.golang.org/protobuf/proto"

	"github.com/gq-godark/gdx-go-sdk/internal/hpke"
	edgepb "github.com/gq-godark/gdx-go-sdk/proto/gdx/edge/v1"
)

// DecodedBinary is a decoded TradingWsBinaryFrame body variant.
type DecodedBinary struct {
	Kind DecodedKind

	EncryptedPush  *edgepb.EncryptedEdgeResponse
	EncryptedOrder *edgepb.EncryptedEdgeRequest
	HpkeSetup      *edgepb.HpkeSetup
	HpkeSetupReply *edgepb.HpkeSetupReply
}

type DecodedKind int

const (
	DecodedIgnored DecodedKind = iota
	DecodedEncryptedPush
	DecodedEncryptedOrder
	DecodedHpkeSetup
	DecodedHpkeSetupReply
)

// EncodeHpkeSetup builds a TradingWsBinaryFrame with an HpkeSetup body.
func EncodeHpkeSetup(userUUID []byte, connID uint64, encappedKey []byte) ([]byte, error) {
	frame := &edgepb.TradingWsBinaryFrame{
		Body: &edgepb.TradingWsBinaryFrame_HpkeSetup{
			HpkeSetup: &edgepb.HpkeSetup{
				UserUuid:    userUUID,
				ConnId:      connID,
				EncappedKey: encappedKey,
			},
		},
	}
	return proto.Marshal(frame)
}

// EncodeHpkeSetupReply builds a TradingWsBinaryFrame with an HpkeSetupReply body.
func EncodeHpkeSetupReply(connID uint64, established bool) ([]byte, error) {
	frame := &edgepb.TradingWsBinaryFrame{
		Body: &edgepb.TradingWsBinaryFrame_HpkeSetupReply{
			HpkeSetupReply: &edgepb.HpkeSetupReply{
				ConnId:      connID,
				Established: established,
			},
		},
	}
	return proto.Marshal(frame)
}

// EncryptedOrderRequest wraps header + ciphertext for the wire envelope.
func EncryptedOrderRequest(header *edgepb.OrderHeader, encryptedBody []byte) *edgepb.EncryptedEdgeRequest {
	return &edgepb.EncryptedEdgeRequest{
		Version:       hpke.WireVersion,
		Header:        header,
		EncryptedBody: encryptedBody,
	}
}

// EncodeEncryptedOrder builds a TradingWsBinaryFrame with an EncryptedOrder body.
func EncodeEncryptedOrder(req *edgepb.EncryptedEdgeRequest) ([]byte, error) {
	frame := &edgepb.TradingWsBinaryFrame{
		Body: &edgepb.TradingWsBinaryFrame_EncryptedOrder{
			EncryptedOrder: req,
		},
	}
	return proto.Marshal(frame)
}

// EncodeEncryptedPush builds a TradingWsBinaryFrame with an EncryptedPush body.
func EncodeEncryptedPush(resp *edgepb.EncryptedEdgeResponse) ([]byte, error) {
	frame := &edgepb.TradingWsBinaryFrame{
		Body: &edgepb.TradingWsBinaryFrame_EncryptedPush{
			EncryptedPush: resp,
		},
	}
	return proto.Marshal(frame)
}

// DecodeBinaryFrame parses a TradingWsBinaryFrame and returns the body variant.
func DecodeBinaryFrame(bytes []byte) (*DecodedBinary, error) {
	frame := &edgepb.TradingWsBinaryFrame{}
	if err := proto.Unmarshal(bytes, frame); err != nil {
		return &DecodedBinary{Kind: DecodedIgnored}, nil
	}
	switch body := frame.Body.(type) {
	case *edgepb.TradingWsBinaryFrame_EncryptedPush:
		return &DecodedBinary{Kind: DecodedEncryptedPush, EncryptedPush: body.EncryptedPush}, nil
	case *edgepb.TradingWsBinaryFrame_HpkeSetupReply:
		return &DecodedBinary{Kind: DecodedHpkeSetupReply, HpkeSetupReply: body.HpkeSetupReply}, nil
	case *edgepb.TradingWsBinaryFrame_EncryptedOrder:
		return &DecodedBinary{Kind: DecodedEncryptedOrder, EncryptedOrder: body.EncryptedOrder}, nil
	case *edgepb.TradingWsBinaryFrame_HpkeSetup:
		return &DecodedBinary{Kind: DecodedHpkeSetup, HpkeSetup: body.HpkeSetup}, nil
	default:
		return &DecodedBinary{Kind: DecodedIgnored}, nil
	}
}

// EncryptedPushToMessage maps an EncryptedEdgeResponse to the legacy JSON-shaped
// transport.Message the client decrypt/dispatch path expects.
func EncryptedPushToMessage(push *edgepb.EncryptedEdgeResponse) map[string]any {
	h := push.GetHeader()
	if h == nil {
		return nil
	}
	messageType := responseMessageTypeString(int32(h.GetMessageType()))
	corr := correlationIDJSON(h.GetCorrelationId())
	return map[string]any{
		"type":           "encrypted_push",
		"message_type":   messageType,
		"encrypted_body": base64.StdEncoding.EncodeToString(push.GetEncryptedBody()),
		"nonce":          h.GetNonce(),
		"fencing_epoch":  h.GetFencingEpoch(),
		"correlation_id": corr,
		"session_seq":    h.GetSessionSeq(),
		"conn_id":        h.GetConnId(),
		"body_length":    h.GetBodyLength(),
	}
}

func responseMessageTypeString(mt int32) string {
	switch mt {
	case 1:
		return "order_update"
	case 2:
		return "system_health"
	case 3:
		return "ack"
	case 4:
		return "open_orders_snapshot"
	case 5:
		return "positions_snapshot"
	case 6:
		return "balance_and_position"
	case 7:
		return "account_margin_update"
	case 8:
		return "mass_quote_ack"
	case 9:
		return "batch_cancel_ack"
	case 10:
		return "batch_modify_ack"
	case 11:
		return "tpsl_update"
	case 12:
		return "leverage_settings"
	case 13:
		return "cancel_all_ack"
	case 14:
		return "close_all_ack"
	case 15:
		return "reverse_ack"
	case 16:
		return "tpsl_ack"
	default:
		return "unknown"
	}
}

func correlationIDJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var buf [16]byte
	n := len(raw)
	if n > 16 {
		n = 16
	}
	copy(buf[16-n:], raw[:n])
	return hex.EncodeToString(buf[:])
}

// CorrelationKeyFromBytes returns the canonical lowercase-hex key used by the
// transport for routing encrypted command acks.
func CorrelationKeyFromBytes(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var buf [16]byte
	n := len(raw)
	if n > 16 {
		n = 16
	}
	copy(buf[16-n:], raw[:n])
	return hex.EncodeToString(buf[:])
}
