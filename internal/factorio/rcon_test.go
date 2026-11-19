package factorio

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRCON is a minimal Factorio-like RCON server: it answers the auth
// packet (id -1 for a wrong password) and exactly one type 0 reply per
// command. Any other packet type is recorded as invalid, as Factorio logs
// "RCON received an invalid message type".
type fakeRCON struct {
	addr     string
	password string
	reply    func(command string) (string, bool) // false = never answer

	mu        sync.Mutex
	commands  []string
	conns     int
	invalid   int
	dropAfter int // close each connection after this many commands; 0 = never
}

func startFakeRCON(t *testing.T, password string, reply func(string) (string, bool)) *fakeRCON {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRCON{addr: ln.Addr().String(), password: password, reply: reply}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeRCON) serve(c net.Conn) {
	defer c.Close()
	f.mu.Lock()
	f.conns++
	f.mu.Unlock()
	id, typ, body, err := readPacket(c)
	if err != nil || typ != rconTypeAuth {
		return
	}
	if body != f.password {
		writePacket(c, -1, rconTypeAuthReply, "")
		return
	}
	writePacket(c, id, rconTypeAuthReply, "")
	for n := 1; ; n++ {
		id, typ, body, err := readPacket(c)
		if err != nil {
			return
		}
		f.mu.Lock()
		if typ != rconTypeExec {
			f.invalid++
			f.mu.Unlock()
			continue
		}
		f.commands = append(f.commands, body)
		drop := f.dropAfter > 0 && n >= f.dropAfter
		f.mu.Unlock()
		if out, ok := f.reply(body); ok {
			writePacket(c, id, rconTypeReply, out)
		}
		if drop {
			return
		}
	}
}

func (f *fakeRCON) stats() (conns, invalid int, commands []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns, f.invalid, append([]string{}, f.commands...)
}

// factorioReplies mimics the replies listed in FACTS.
func factorioReplies(command string) (string, bool) {
	switch command {
	case "/version":
		return "2.1.24\n", true
	case "/players online":
		return "Online players (2):\n  alice (online)\n  bob (online)\n", true
	case "/hang":
		return "", false
	}
	return `Unknown command "` + strings.TrimPrefix(command, "/") + `". Type /h for help.`, true
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestRCONExecReusesConnection(t *testing.T) {
	f := startFakeRCON(t, "pw", factorioReplies)
	r := NewRCON(f.addr, "pw")
	defer r.Close()

	for i := 0; i < 3; i++ {
		got, err := r.Exec(testCtx(t), "/version")
		if err != nil {
			t.Fatal(err)
		}
		if got != "2.1.24\n" {
			t.Fatalf("reply %q", got)
		}
	}
	conns, invalid, commands := f.stats()
	if conns != 1 {
		t.Errorf("connections = %d, want 1", conns)
	}
	if invalid != 0 {
		t.Errorf("%d invalid packets sent (sentinel?)", invalid)
	}
	if len(commands) != 3 {
		t.Errorf("commands = %q", commands)
	}
}

func TestRCONBadPassword(t *testing.T) {
	f := startFakeRCON(t, "pw", factorioReplies)
	r := NewRCON(f.addr, "wrong")
	defer r.Close()
	_, err := r.Exec(testCtx(t), "/version")
	if !errors.Is(err, ErrRCONAuth) {
		t.Fatalf("err = %v, want ErrRCONAuth", err)
	}
}

func TestRCONReconnectsAfterFailure(t *testing.T) {
	f := startFakeRCON(t, "pw", factorioReplies)
	f.mu.Lock()
	f.dropAfter = 1
	f.mu.Unlock()
	r := NewRCON(f.addr, "pw")
	defer r.Close()

	if _, err := r.Exec(testCtx(t), "/version"); err != nil {
		t.Fatal(err)
	}
	// The server closed the connection: this command fails, the next one
	// reconnects.
	if _, err := r.Exec(testCtx(t), "/version"); err == nil {
		t.Fatal("expected an error on the dropped connection")
	}
	got, err := r.Exec(testCtx(t), "/version")
	if err != nil {
		t.Fatal(err)
	}
	if got != "2.1.24\n" {
		t.Fatalf("reply %q", got)
	}
	if conns, _, _ := f.stats(); conns != 2 {
		t.Errorf("connections = %d, want 2", conns)
	}
}

func TestRCONContextBoundsRead(t *testing.T) {
	f := startFakeRCON(t, "pw", factorioReplies)
	r := NewRCON(f.addr, "pw")
	defer r.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.Exec(ctx, "/hang")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("Exec did not honour the context")
	}
	// The client recovers with a new connection.
	if _, err := r.Exec(testCtx(t), "/version"); err != nil {
		t.Fatal(err)
	}
}

func TestRCONCancelUnblocks(t *testing.T) {
	f := startFakeRCON(t, "pw", factorioReplies)
	r := NewRCON(f.addr, "pw")
	defer r.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := r.Exec(ctx, "/hang")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}

func TestRCONUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	r := NewRCON(addr, "pw")
	if _, err := r.Exec(testCtx(t), "/version"); err == nil {
		t.Fatal("expected a connection error")
	}
}

func TestRCONPacketRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go writePacket(a, 7, rconTypeExec, "/players online")
	id, typ, body, err := readPacket(b)
	if err != nil {
		t.Fatal(err)
	}
	if id != 7 || typ != rconTypeExec || body != "/players online" {
		t.Fatalf("got %d %d %q", id, typ, body)
	}
}

func TestRCONRejectsBadPacketSize(t *testing.T) {
	for _, head := range [][]byte{{1, 0, 0, 0}, {0xff, 0xff, 0xff, 0x7f}} {
		if _, _, _, err := readPacket(strings.NewReader(string(head))); err == nil {
			t.Errorf("size %v accepted", head)
		}
	}
}
