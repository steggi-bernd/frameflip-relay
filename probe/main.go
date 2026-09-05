// Prueft einen laufenden Relay so, wie ihn FrameFlip und das Handy benutzen: von
// aussen, ueber TLS, durch den Reverse-Proxy hindurch.
//
// Die Tests in relay_test.go laufen gegen einen Server im selben Prozess. Was sie
// nicht abdecken, ist der Weg dorthin - Zertifikat, Proxy, WebSocket-Upgrade ueber
// die echte Domain. Genau dort geht bei einer Inbetriebnahme etwas schief.
//
//	go run ./probe                                  # gegen localhost
//	go run ./probe wss://relay.example.org          # gegen die Installation
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/coder/websocket"
)

// So gross, wie RELAY_MAX_MESSAGE es zulaesst. Die Voreinstellung der Bibliothek
// sind 32 KB - ein Vorschaubild waere damit zu gross.
const readLimit = 1 << 20

var base = "ws://localhost:8080"

func main() {
	if len(os.Args) > 1 {
		base = os.Args[1]
	}

	room := make([]byte, 16)
	if _, err := rand.Read(room); err != nil {
		fail("random room id", err)
	}
	id := hex.EncodeToString(room)

	fmt.Printf("%s, room %s\n\n", base, id)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	started := time.Now()

	host, err := dial(ctx, id, "host")
	if err != nil {
		fail("host connects", err)
	}
	defer host.CloseNow()

	fmt.Printf("connected in %v\n", time.Since(started).Round(time.Millisecond))
	expect(ctx, host, websocket.MessageText, `{"t":"waiting"}`, "host waits")

	client, err := dial(ctx, id, "client")
	if err != nil {
		fail("client connects", err)
	}
	defer client.CloseNow()

	expect(ctx, host, websocket.MessageText, `{"t":"peer","up":true}`, "host learns of the client")
	expect(ctx, client, websocket.MessageText, `{"t":"peer","up":true}`, "client learns of the host")

	// Eine Nutzlast, die mit '{' beginnt - genau der Fall, an dem eine Erkennung am
	// Inhalt scheitern wuerde.
	tricky := []byte(`{"t":"peer","up":false}`)

	send(ctx, host, tricky)
	expect(ctx, client, websocket.MessageBinary, string(tricky), "payload starting with { stays binary")

	send(ctx, client, []byte("back"))
	expect(ctx, host, websocket.MessageBinary, "back", "the other direction")

	// So gross wie ein Vorschaubild. Hier faellt auf, wenn ein Proxy dazwischen
	// puffert oder eine Groessengrenze zu eng steht.
	big := make([]byte, 250*1024)
	if _, err := rand.Read(big); err != nil {
		fail("random payload", err)
	}

	before := time.Now()
	send(ctx, host, big)
	expectSize(ctx, client, len(big), "250 KB gets through")
	fmt.Printf("       250 KB in %v\n", time.Since(before).Round(time.Millisecond))

	// Eine belegte Rolle muss abgewiesen werden, nicht uebernommen.
	if second, err := dial(ctx, id, "host"); err == nil {
		expect(ctx, second, websocket.MessageText, `{"t":"error","why":"role already taken"}`,
			"a second host is refused")
		second.CloseNow()
	} else {
		fmt.Println("  ok   a second host is refused (at connect)")
	}

	// Geht der Client, erfaehrt es der Host.
	client.Close(websocket.StatusNormalClosure, "")
	expect(ctx, host, websocket.MessageText, `{"t":"peer","up":false}`, "departure is announced")

	fmt.Printf("\nall good, %v total\n", time.Since(started).Round(time.Millisecond))
}

func dial(ctx context.Context, id, role string) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, fmt.Sprintf("%s/r/%s?role=%s", base, id, role), nil)
	if conn != nil {
		conn.SetReadLimit(readLimit)
	}

	return conn, err
}

func send(ctx context.Context, conn *websocket.Conn, data []byte) {
	if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		fail("send", err)
	}
}

func expect(ctx context.Context, conn *websocket.Conn, kind websocket.MessageType, want, what string) {
	gotKind, data := receive(ctx, conn, what)

	if gotKind != kind {
		stop(what, fmt.Sprintf("frame type %v instead of %v", gotKind, kind))
	}

	if string(data) != want {
		stop(what, fmt.Sprintf("%q instead of %q", string(data), want))
	}

	fmt.Printf("  ok   %s\n", what)
}

func expectSize(ctx context.Context, conn *websocket.Conn, size int, what string) {
	kind, data := receive(ctx, conn, what)

	if kind != websocket.MessageBinary || len(data) != size {
		stop(what, fmt.Sprintf("%d bytes as %v", len(data), kind))
	}

	fmt.Printf("  ok   %s\n", what)
}

func receive(ctx context.Context, conn *websocket.Conn, what string) (websocket.MessageType, []byte) {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	kind, data, err := conn.Read(c)
	if err != nil {
		fail(what, err)
	}

	return kind, data
}

func fail(what string, err error) {
	stop(what, err.Error())
}

func stop(what, why string) {
	fmt.Printf("  FAIL %s: %s\n", what, why)
	os.Exit(1)
}
