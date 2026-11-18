package csgdlink_test

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"testing"
	"time"

	"code-contract/pkg/csgdlink"
)

func TestDlinkFrameAndInstanceCodec(t *testing.T) {
	t.Run("CStringTruncatesAtFirstNUL", func(t *testing.T) {
		raw := make([]byte, 64)
		copy(raw, "OrderService-OrderService")
		raw[len("OrderService-OrderService")] = 0
		// Trailing dirty bytes after NUL must be discarded.
		copy(raw[30:], "DIRTY_GARBAGE")
		if got := csgdlink.CString(raw); got != "OrderService-OrderService" {
			t.Fatalf("CString = %q, want %q", got, "OrderService-OrderService")
		}
	})

	t.Run("FrameAnd248BInstanceRoundTrip", func(t *testing.T) {
		inst := csgdlink.Instance{
			ClientID: 0x0102030405060708,
			Version:  42,
			ProcID:   12345,
			OsNodeID: 7,
			Name32:   "CompatName",
			Addr:     "tcp://ipv4:127.0.0.1:18500",
			Name64:   "OrderService-OrderService",
		}
		encodedInst := csgdlink.EncodeInstance(inst)
		if len(encodedInst) != csgdlink.ServiceInstLen {
			t.Fatalf("encoded instance len = %d, want %d", len(encodedInst), csgdlink.ServiceInstLen)
		}
		decodedInst, err := csgdlink.ParseInstance(encodedInst)
		if err != nil {
			t.Fatalf("ParseInstance failed: %v", err)
		}
		if decodedInst != inst {
			t.Fatalf("decoded instance = %+v, want %+v", decodedInst, inst)
		}

		subReq := csgdlink.SubRequest{
			ClientID: 99,
			Name:     "OrderService-OrderService",
		}
		csgdlink.MergeTransMask(&subReq.Trans, csgdlink.TransMaskForAddr("tcp://ipv4:127.0.0.1:80"))
		csgdlink.MergeTransMask(&subReq.Trans, csgdlink.TransMaskForAddr("dophi_mesh://ipv4:127.0.0.1:80"))
		rawSub := csgdlink.EncodeSubRequest(subReq)
		if len(rawSub) != csgdlink.SubPayloadLen {
			t.Fatalf("sub payload len = %d, want %d", len(rawSub), csgdlink.SubPayloadLen)
		}
		gotSub, err := csgdlink.DecodeSubRequest(rawSub)
		if err != nil {
			t.Fatalf("DecodeSubRequest failed: %v", err)
		}
		if gotSub.ClientID != subReq.ClientID || gotSub.Name != subReq.Name || gotSub.Trans[1] != 1 || gotSub.Trans[4] != 1 {
			t.Fatalf("DecodeSubRequest mismatch: %+v", gotSub)
		}

		var buf bytes.Buffer
		f := csgdlink.NewRequestFrame(csgdlink.SubTypeSub, rawSub, 77)
		if err := csgdlink.WriteFrame(&buf, f, rawSub); err != nil {
			t.Fatalf("WriteFrame failed: %v", err)
		}
		gotFrame, gotBody, err := csgdlink.ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame failed: %v", err)
		}
		if gotFrame.Magic != csgdlink.DlinkMagic || gotFrame.SubType != csgdlink.SubTypeSub || gotFrame.CorrID != 77 {
			t.Fatalf("ReadFrame header mismatch: %+v", gotFrame)
		}
		if !bytes.Equal(gotBody, rawSub) {
			t.Fatalf("ReadFrame body mismatch")
		}
	})

	t.Run("RewriteMeshAddrToTCP", func(t *testing.T) {
		got := csgdlink.RewriteMeshAddrToTCP("dophi_mesh://ipv4:10.29.92.80:33001")
		if got != "tcp://ipv4:10.29.92.80:33001" {
			t.Fatalf("RewriteMeshAddrToTCP = %q", got)
		}
		if untouched := csgdlink.RewriteMeshAddrToTCP("tcp://ipv4:127.0.0.1:18500"); untouched != "tcp://ipv4:127.0.0.1:18500" {
			t.Fatalf("untouched TCP addr = %q", untouched)
		}
	})
}

func TestDlinkClient_NotifyInterleavedInRoundTrip(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	client := csgdlink.NewClient(clientConn)
	var notified []csgdlink.Frame
	client.SetNotifyHandler(func(f csgdlink.Frame, body []byte) {
		notified = append(notified, f)
	})

	errCh := make(chan error, 1)
	go func() {
		// 1) Expect CLIENT_ID with os.Getpid()
		reqFrame, reqBody, err := csgdlink.ReadFrame(serverConn)
		if err != nil {
			errCh <- err
			return
		}
		if reqFrame.SubType != csgdlink.SubTypeClientID {
			errCh <- os.ErrInvalid
			return
		}
		gotPID := binary.BigEndian.Uint32(reqBody[0:4])
		if gotPID != uint32(os.Getpid()) {
			errCh <- os.ErrInvalid
			return
		}
		// Send an interleaved NOTIFY before CLIENT_ID_ACK
		notifyFrame := csgdlink.Frame{
			Magic:   csgdlink.DlinkMagic,
			Type:    csgdlink.MsgTypeNotify,
			SubType: csgdlink.NotifySubTypePubUpdate,
			LinkID:  csgdlink.LinkID,
		}
		if err := csgdlink.WriteFrame(serverConn, notifyFrame, make([]byte, 16)); err != nil {
			errCh <- err
			return
		}
		ack := make([]byte, 16)
		binary.BigEndian.PutUint64(ack[8:], 0xCAFEBABE)
		replyFrame := csgdlink.Frame{
			Magic:   csgdlink.DlinkMagic,
			Type:    csgdlink.MsgTypeReply,
			SubType: csgdlink.SubTypeClientIDAck,
			CorrID:  reqFrame.CorrID,
			LinkID:  reqFrame.LinkID,
		}
		errCh <- csgdlink.WriteFrame(serverConn, replyFrame, ack)
	}()

	cid, err := client.ClientID(2 * time.Second)
	if err != nil {
		t.Fatalf("ClientID failed with interleaved NOTIFY: %v", err)
	}
	if cid != 0xCAFEBABE {
		t.Fatalf("clientID = %#x, want 0xCAFEBABE", cid)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("mock pipe peer error: %v", err)
	}
	if len(notified) != 1 || notified[0].Type != csgdlink.MsgTypeNotify {
		t.Fatalf("expected 1 NOTIFY callback, got %+v", notified)
	}
}

func TestDlinkObserver_NonIntrusiveSubAckCapture(t *testing.T) {
	obs := csgdlink.NewObserver()

	inst := csgdlink.Instance{
		ClientID: 101,
		ProcID:   8888,
		Name64:   "PaymentService-PaymentService",
		Addr:     "tcp://ipv4:127.0.0.1:29001",
	}
	subAckBody := make([]byte, 16+csgdlink.ServiceInstLen)
	binary.BigEndian.PutUint32(subAckBody[0:], 1)
	binary.BigEndian.PutUint32(subAckBody[4:], 0)
	binary.BigEndian.PutUint32(subAckBody[8:], csgdlink.ServiceInstLen)
	copy(subAckBody[16:], csgdlink.EncodeInstance(inst))

	var wire bytes.Buffer
	ackFrame := csgdlink.Frame{
		Magic:   csgdlink.DlinkMagic,
		Type:    csgdlink.MsgTypeReply,
		SubType: csgdlink.SubTypeSubAck,
		CorrID:  9,
		LinkID:  csgdlink.LinkID,
	}
	if err := csgdlink.WriteFrame(&wire, ackFrame, subAckBody); err != nil {
		t.Fatalf("WriteFrame failed: %v", err)
	}
	raw := wire.Bytes()

	// Feed split chunks to verify stateful assembly
	var events []csgdlink.ObservedEvent
	events = append(events, obs.Feed("csg->client", raw[:17])...)
	if len(events) != 0 {
		t.Fatalf("expected 0 events before full frame, got %d", len(events))
	}
	events = append(events, obs.Feed("csg->client", raw[17:])...)
	if len(events) != 1 {
		t.Fatalf("expected 1 observed event, got %d", len(events))
	}
	ev := events[0]
	if ev.SubType != csgdlink.SubTypeSubAck || len(ev.Instances) != 1 {
		t.Fatalf("unexpected observed event: %+v", ev)
	}
	if ev.Instances[0].Name64 != "PaymentService-PaymentService" || ev.Instances[0].Addr != "tcp://ipv4:127.0.0.1:29001" {
		t.Fatalf("instance mismatch: %+v", ev.Instances[0])
	}
}

func TestResolveServiceTCP_Validation(t *testing.T) {
	if _, err := csgdlink.ResolveServiceTCP("", "DemoSvc", time.Second); err == nil {
		t.Fatal("expected error for empty csgAddr")
	}
	if _, err := csgdlink.ResolveServiceTCP("127.0.0.1:31806", "", time.Second); err == nil {
		t.Fatal("expected error for empty serviceName")
	}
}
