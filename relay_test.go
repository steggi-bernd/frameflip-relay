package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testConfig() config {
	return config{
		maxMessage:   1 << 20,
		sendQueue:    8,
		idleTimeout:  5 * time.Second,
		pingInterval: 1 * time.Second,
	}
}

func startRelay(t *testing.T) (string, *hub) {
	t.Helper()

	cfg := testConfig()
	h := newHub(cfg.sendQueue)

	mux := http.NewServeMux()
	mux.HandleFunc("/r/", newRelayHandler(h, cfg))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return "ws" + strings.TrimPrefix(server.URL, "http"), h
}

const testRoom = "0123456789abcdef0123456789abcdef"

func dial(t *testing.T, base, room string, who role) *websocket.Conn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, base+"/r/"+room+"?role="+string(who), nil)
	if err != nil {
		t.Fatalf("dial %s: %v", who, err)
	}

	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func read(t *testing.T, conn *websocket.Conn) (websocket.MessageType, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	kind, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	return kind, string(data)
}

func write(t *testing.T, conn *websocket.Conn, kind websocket.MessageType, data []byte) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := conn.Write(ctx, kind, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// Der Normalfall: Host wartet, Client kommt dazu, beide erfahren es, Bytes gehen
// in beide Richtungen unveraendert durch.
func TestForwardsBothWays(t *testing.T) {
	base, _ := startRelay(t)

	host := dial(t, base, testRoom, roleHost)

	if _, msg := read(t, host); msg != `{"t":"waiting"}` {
		t.Fatalf("erwartet waiting, bekam %s", msg)
	}

	client := dial(t, base, testRoom, roleClient)

	if _, msg := read(t, host); msg != `{"t":"peer","up":true}` {
		t.Fatalf("Host erfaehrt den Client nicht: %s", msg)
	}
	if _, msg := read(t, client); msg != `{"t":"peer","up":true}` {
		t.Fatalf("Client erfaehrt den Host nicht: %s", msg)
	}

	// Nutzlast, die mit '{' beginnt - genau der Fall, an dem eine Erkennung am
	// Inhalt scheitern wuerde.
	tricky := []byte(`{"t":"peer","up":false}`)

	write(t, host, websocket.MessageBinary, tricky)

	kind, msg := read(t, client)
	if kind != websocket.MessageBinary {
		t.Fatalf("Nutzlast kam als %v an, nicht binaer", kind)
	}
	if msg != string(tricky) {
		t.Fatalf("Nutzlast veraendert: %s", msg)
	}

	write(t, client, websocket.MessageBinary, []byte("zurueck"))

	if _, msg := read(t, host); msg != "zurueck" {
		t.Fatalf("Rueckweg: %s", msg)
	}
}

// Eine belegte Rolle wird abgewiesen, nicht uebernommen. Sonst koennte jeder, der
// die Raumkennung kennt, den echten PC verdraengen.
func TestSecondHostRefused(t *testing.T) {
	base, _ := startRelay(t)

	first := dial(t, base, testRoom, roleHost)
	read(t, first)

	second := dial(t, base, testRoom, roleHost)

	kind, msg := read(t, second)
	if kind != websocket.MessageText || !strings.Contains(msg, "error") {
		t.Fatalf("zweiter Host haette abgewiesen werden muessen: %s", msg)
	}

	// Der erste laeuft weiter - er darf davon nichts merken.
	client := dial(t, base, testRoom, roleClient)
	if _, msg := read(t, first); msg != `{"t":"peer","up":true}` {
		t.Fatalf("der erste Host ist beschaedigt: %s", msg)
	}

	_ = client
}

// Geht eine Seite, erfaehrt die andere es - und der Raum verschwindet.
func TestPeerLeaves(t *testing.T) {
	base, h := startRelay(t)

	host := dial(t, base, testRoom, roleHost)
	read(t, host)

	client := dial(t, base, testRoom, roleClient)
	read(t, host)
	read(t, client)

	if h.count() != 1 {
		t.Fatalf("erwartet 1 Raum, sind %d", h.count())
	}

	_ = client.Close(websocket.StatusNormalClosure, "")

	if _, msg := read(t, host); msg != `{"t":"peer","up":false}` {
		t.Fatalf("Abgang wird nicht gemeldet: %s", msg)
	}

	_ = host.Close(websocket.StatusNormalClosure, "")

	// Der Raum raeumt sich selbst weg - sonst waere jeder je benutzte Raum ein
	// Eintrag, den niemand mehr entfernt.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.count() != 0 {
		time.Sleep(20 * time.Millisecond)
	}

	if h.count() != 0 {
		t.Fatalf("Raum blieb stehen: %d", h.count())
	}
}

// Textframes gehoeren dem Relay und werden nie weitergereicht.
func TestTextIsNotForwarded(t *testing.T) {
	base, _ := startRelay(t)

	host := dial(t, base, testRoom, roleHost)
	read(t, host)

	client := dial(t, base, testRoom, roleClient)
	read(t, host)
	read(t, client)

	write(t, host, websocket.MessageText, []byte(`{"t":"peer","up":false}`))
	write(t, host, websocket.MessageBinary, []byte("danach"))

	// Kommt "danach" an, ist der Textframe unterwegs verworfen worden.
	if _, msg := read(t, client); msg != "danach" {
		t.Fatalf("Textframe wurde weitergereicht: %s", msg)
	}
}

// Unsinnige Adressen werden gar nicht erst zu Raeumen.
func TestRejectsBadAddresses(t *testing.T) {
	for _, path := range []string{
		"/r/",
		"/r/zu-kurz",
		"/r/0123456789ABCDEF0123456789ABCDEF",  // Grossbuchstaben
		"/r/0123456789abcdef0123456789abcdeg",  // kein Hex
		"/r/0123456789abcdef0123456789abcdef0", // zu lang
	} {
		if _, ok := roomFrom(path); ok {
			t.Errorf("%q haette abgelehnt werden muessen", path)
		}
	}

	if id, ok := roomFrom("/r/" + testRoom); !ok || id != testRoom {
		t.Errorf("gueltige Kennung wurde abgelehnt")
	}

	for _, value := range []string{"", "hosts", "HOST", "relay"} {
		if _, ok := roleFrom(value); ok {
			t.Errorf("Rolle %q haette abgelehnt werden muessen", value)
		}
	}
}

// Eine Fehlermeldung darf den umgebenden JSON-String nicht sprengen.
func TestSanitize(t *testing.T) {
	got := string(controlError(`weg"; drop"` + "\n"))
	want := `{"t":"error","why":"weg; drop"}`

	if got != want {
		t.Fatalf("sanitize: %s", got)
	}
}
