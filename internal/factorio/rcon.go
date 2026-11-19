package factorio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// ErrRCONAuth reports a rejected RCON password (reply id -1).
var ErrRCONAuth = errors.New("rcon: authentication failed")

// Source RCON packet types.
const (
	rconTypeAuth      int32 = 3 // request: authenticate with the password
	rconTypeAuthReply int32 = 2 // reply to rconTypeAuth
	rconTypeExec      int32 = 2 // request: run a command
	rconTypeReply     int32 = 0 // reply to rconTypeExec

	// rconMaxPacket bounds the size field of a received packet.
	rconMaxPacket = 4 << 20
)

// RCON is a Source RCON client for Factorio's --rcon-bind interface. One
// TCP connection is kept open and reused; mu serialises commands (one
// request, exactly one reply packet with the same id: no sentinel packets,
// FACTS). On any I/O error the connection is closed and the next Exec
// reconnects and re-authenticates once.
type RCON struct {
	addr     string
	password string
	mu       sync.Mutex
	conn     net.Conn
	nextID   int32
}

// NewRCON returns a client for addr ("127.0.0.1:<rconPort>"). It does not
// connect.
func NewRCON(addr, password string) *RCON {
	return &RCON{addr: addr, password: password}
}

// Exec sends command and returns the reply body. ctx bounds connect, write
// and read (deadline on the connection).
func (r *RCON) Exec(ctx context.Context, command string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r.conn == nil {
		if err := r.connect(ctx); err != nil {
			return "", err
		}
	}
	reply, err := r.roundTrip(ctx, rconTypeExec, command)
	if err != nil {
		r.closeConn()
		return "", fmt.Errorf("rcon: %w", err)
	}
	return reply, nil
}

// Close closes the connection (if any). The client can be reused.
func (r *RCON) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeConn()
}

// connect dials and authenticates. On failure no connection is kept.
func (r *RCON) connect(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", r.addr)
	if err != nil {
		return fmt.Errorf("rcon: %w", err)
	}
	r.conn = conn
	if _, err := r.roundTrip(ctx, rconTypeAuth, r.password); err != nil {
		r.closeConn()
		if errors.Is(err, ErrRCONAuth) {
			return err
		}
		return fmt.Errorf("rcon: authenticating: %w", err)
	}
	return nil
}

// closeConn closes and forgets the connection. r.mu must be held.
func (r *RCON) closeConn() error {
	if r.conn == nil {
		return nil
	}
	err := r.conn.Close()
	r.conn = nil
	return err
}

// roundTrip sends one packet and reads until its reply: for rconTypeAuth
// the auth reply (id -1 = ErrRCONAuth), otherwise the type 0 packet with
// the same id. Packets that do not belong to the request are skipped.
// r.mu must be held and r.conn set.
func (r *RCON) roundTrip(ctx context.Context, typ int32, body string) (string, error) {
	conn := r.conn
	deadline, _ := ctx.Deadline() // zero time = no deadline
	if err := conn.SetDeadline(deadline); err != nil {
		return "", err
	}
	// Cancelling ctx unblocks a pending read or write.
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	r.nextID++
	if r.nextID <= 0 {
		r.nextID = 1
	}
	id := r.nextID
	if err := writePacket(conn, id, typ, body); err != nil {
		return "", contextError(ctx, err)
	}
	for {
		gotID, gotType, gotBody, err := readPacket(conn)
		if err != nil {
			return "", contextError(ctx, err)
		}
		if typ == rconTypeAuth {
			if gotType != rconTypeAuthReply {
				continue // some servers send an empty reply before the auth reply
			}
			if gotID == -1 {
				return "", ErrRCONAuth
			}
			if gotID != id {
				return "", fmt.Errorf("unexpected auth reply id %d", gotID)
			}
			return "", nil
		}
		if gotID == id && gotType == rconTypeReply {
			return gotBody, nil
		}
	}
}

// contextError prefers the context's error over the I/O error it caused.
// The connection deadline equals the context deadline, so a timeout can be
// reported just before ctx.Err is set.
func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
	}
	return err
}

// writePacket writes one Source RCON packet: size, id, type (int32 little
// endian), body and two NUL bytes.
func writePacket(w io.Writer, id, typ int32, body string) error {
	buf := make([]byte, 14+len(body))
	binary.LittleEndian.PutUint32(buf[0:], uint32(10+len(body)))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], body)
	_, err := w.Write(buf)
	return err
}

// readPacket reads one Source RCON packet.
func readPacket(r io.Reader) (id, typ int32, body string, err error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, 0, "", err
	}
	size := binary.LittleEndian.Uint32(head[:])
	if size < 10 || size > rconMaxPacket {
		return 0, 0, "", fmt.Errorf("invalid packet size %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, 0, "", err
	}
	id = int32(binary.LittleEndian.Uint32(buf[0:]))
	typ = int32(binary.LittleEndian.Uint32(buf[4:]))
	body = strings.TrimRight(string(buf[8:]), "\x00")
	return id, typ, body, nil
}
