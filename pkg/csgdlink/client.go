package csgdlink

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// NotifyHandler receives asynchronous MsgTypeNotify pushes pushed by CSG.
type NotifyHandler func(frame Frame, body []byte)

// Client speaks the stateful CSG dlink control-plane protocol over TCP.
type Client struct {
	conn     net.Conn
	mu       sync.Mutex
	uuid     uint32
	clientID uint64
	corrID   uint32
	onNotify NotifyHandler
}

// NewClient wraps an existing net.Conn (primarily useful with net.Pipe in tests).
func NewClient(conn net.Conn) *Client {
	return &Client{conn: conn}
}

// Dial connects to a CSG dlink endpoint over plain TCP.
func Dial(addr string, timeout time.Duration) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

// SetNotifyHandler installs an asynchronous handler invoked for MsgTypeNotify frames.
func (c *Client) SetNotifyHandler(fn NotifyHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onNotify = fn
}

// Close closes the underlying dlink TCP connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Register sends DDM_DLINK REGISTER (0xFF00) and stores the assigned channel UUID.
func (c *Client) Register(timeout time.Duration) (uint32, error) {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint32(payload[0:4], 0xFFFFFFFF)
	binary.BigEndian.PutUint32(payload[4:8], uint32(os.Getpid()))
	_, body, err := c.roundTrip(SubTypeRegister, payload, SubTypeRegisterAck, timeout)
	if err != nil {
		return 0, err
	}
	if len(body) < 8 {
		return 0, fmt.Errorf("csgdlink: short REGISTER_ACK body %d", len(body))
	}
	ret := binary.BigEndian.Uint32(body[0:4])
	uuid := binary.BigEndian.Uint32(body[4:8])
	if ret != 0 {
		return 0, fmt.Errorf("csgdlink: REGISTER ret=%d", ret)
	}
	c.uuid = uuid
	return uuid, nil
}

// ClientID requests a logical CSG client identifier bound to the current OS PID.
func (c *Client) ClientID(timeout time.Duration) (uint64, error) {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint32(payload[0:4], uint32(os.Getpid()))
	_, body, err := c.roundTrip(SubTypeClientID, payload, SubTypeClientIDAck, timeout)
	if err != nil {
		return 0, err
	}
	if len(body) < 16 {
		return 0, fmt.Errorf("csgdlink: short CLIENT_ID_ACK body %d", len(body))
	}
	rst := binary.BigEndian.Uint32(body[0:4])
	if rst != 0 {
		return 0, fmt.Errorf("csgdlink: CLIENT_ID rst=%d", rst)
	}
	c.clientID = binary.BigEndian.Uint64(body[8:16])
	return c.clientID, nil
}

// LocalClientID returns the allocated CSG client ID.
func (c *Client) LocalClientID() uint64 { return c.clientID }

// Pub publishes a service instance address under svcName.
func (c *Client) Pub(svcName, addr string, timeout time.Duration) error {
	return c.PubInstance(Instance{
		ClientID: c.clientID,
		ProcID:   uint32(os.Getpid()),
		Name32:   svcName,
		Addr:     addr,
		Name64:   svcName,
	}, timeout)
}

// PubInstance publishes an explicit CSG_SERVICE_INST_INFO_S instance.
func (c *Client) PubInstance(inst Instance, timeout time.Duration) error {
	if inst.ClientID == 0 {
		inst.ClientID = c.clientID
	}
	if inst.ProcID == 0 {
		inst.ProcID = uint32(os.Getpid())
	}
	payload := EncodePubRequest(PubRequest{Instance: inst})
	_, body, err := c.roundTrip(SubTypePub, payload, SubTypePubAck, timeout)
	if err != nil {
		return err
	}
	if len(body) < 4 {
		return fmt.Errorf("csgdlink: short PUB_ACK body %d", len(body))
	}
	if rst := binary.BigEndian.Uint32(body[0:4]); rst != 0 {
		return fmt.Errorf("csgdlink: PUB rst=%d", rst)
	}
	return nil
}

// Sub subscribes to svcName across both TCP and dophi_mesh transports and returns matched instances.
func (c *Client) Sub(svcName string, timeout time.Duration) ([]Instance, error) {
	var trans [7]byte
	MergeTransMask(&trans, TransMaskForAddr("tcp://"))
	MergeTransMask(&trans, TransMaskForAddr("dophi_mesh://"))
	return c.SubWithTrans(svcName, trans, timeout)
}

// SubWithTrans sends a SUB request with explicit transport selection flags.
func (c *Client) SubWithTrans(svcName string, trans [7]byte, timeout time.Duration) ([]Instance, error) {
	payload := EncodeSubRequest(SubRequest{
		ClientID: c.clientID,
		Name:     svcName,
		Trans:    trans,
	})
	_, body, err := c.roundTrip(SubTypeSub, payload, SubTypeSubAck, timeout)
	if err != nil {
		return nil, err
	}
	if len(body) < 16 {
		return nil, fmt.Errorf("csgdlink: short SUB_ACK body %d", len(body))
	}
	count := binary.BigEndian.Uint32(body[0:4])
	rst := binary.BigEndian.Uint32(body[4:8])
	if rst != 0 {
		return nil, fmt.Errorf("csgdlink: SUB rst=%d", rst)
	}
	instances := make([]Instance, 0, int(count))
	off := 16
	for i := uint32(0); i < count; i++ {
		if off+ServiceInstLen > len(body) {
			return nil, fmt.Errorf("csgdlink: short SUB_ACK instance %d", i)
		}
		inst, err := ParseInstance(body[off : off+ServiceInstLen])
		if err != nil {
			return nil, err
		}
		instances = append(instances, inst)
		off += ServiceInstLen
	}
	return instances, nil
}

// Unpub unpublishes a previously registered service instance.
func (c *Client) Unpub(svcName, addr string, timeout time.Duration) error {
	inst := Instance{
		ClientID: c.clientID,
		ProcID:   uint32(os.Getpid()),
		Name32:   svcName,
		Addr:     addr,
		Name64:   svcName,
	}
	payload := EncodeInstance(inst)
	_, body, err := c.roundTrip(SubTypeUnpub, payload, SubTypeUnpubAck, timeout)
	if err != nil {
		return err
	}
	if len(body) < 4 {
		return fmt.Errorf("csgdlink: short UNPUB_ACK body %d", len(body))
	}
	if rst := binary.BigEndian.Uint32(body[0:4]); rst != 0 {
		return fmt.Errorf("csgdlink: UNPUB rst=%d", rst)
	}
	return nil
}

// Unsub cancels an active subscription for svcName.
func (c *Client) Unsub(svcName string, timeout time.Duration) error {
	payload := EncodeUnsubRequest(UnsubRequest{
		ClientID: c.clientID,
		Name:     svcName,
	})
	_, body, err := c.roundTrip(SubTypeUnsub, payload, SubTypeUnsubAck, timeout)
	if err != nil {
		return err
	}
	if len(body) < 4 {
		return fmt.Errorf("csgdlink: short UNSUB_ACK body %d", len(body))
	}
	if rst := binary.BigEndian.Uint32(body[0:4]); rst != 0 {
		return fmt.Errorf("csgdlink: UNSUB rst=%d", rst)
	}
	return nil
}

func (c *Client) roundTrip(subType uint16, payload []byte, wantAck uint16, timeout time.Duration) (Frame, []byte, error) {
	c.mu.Lock()
	notifyFn := c.onNotify
	c.mu.Unlock()

	corrID := atomic.AddUint32(&c.corrID, 1)
	req := NewRequestFrame(subType, payload, corrID)

	if timeout > 0 {
		if err := c.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
			return Frame{}, nil, err
		}
		defer func() {
			_ = c.conn.SetDeadline(time.Time{})
		}()
	}

	if err := WriteFrame(c.conn, req, payload); err != nil {
		return Frame{}, nil, err
	}

	for {
		got, body, err := ReadFrame(c.conn)
		if err != nil {
			return Frame{}, nil, err
		}
		// CSG pushes NOTIFY_PUB_UPDATE / NOTIFY_SUBER_QUIT asynchronously on the
		// control channel; consume via callback without breaking in-flight ACKs.
		if got.Type == MsgTypeNotify {
			if notifyFn != nil {
				notifyFn(got, body)
			}
			continue
		}
		if got.SubType != wantAck {
			return Frame{}, nil, fmt.Errorf("csgdlink: unexpected subtype 0x%04X, want 0x%04X", got.SubType, wantAck)
		}
		if got.CorrID != 0 && got.CorrID != corrID {
			continue
		}
		return got, body, nil
	}
}
