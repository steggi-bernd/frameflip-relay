package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Laenge der Raumkennung in Hex-Zeichen: 16 Bytes.
//
// Sie ist kein Geheimnis, sondern ein Name - eine Einwegableitung aus dem
// Kopplungsschluessel. Laenger waere sinnlos, kuerzer liesse sich durchprobieren.
const roomIDLength = 32

func newRelayHandler(h *hub, cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := roomFrom(r.URL.Path)
		if !ok {
			http.Error(w, "bad room", http.StatusBadRequest)
			return
		}

		who, ok := roleFrom(r.URL.Query().Get("role"))
		if !ok {
			http.Error(w, "bad role", http.StatusBadRequest)
			return
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Der Relay hat keine Webseite und keine Cookies; es gibt nichts, was
			// eine fremde Herkunft ausnutzen koennte. Die Nutzlast ist ohnehin
			// verschluesselt und fuer jeden ohne Schluessel wertlos.
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}

		conn.SetReadLimit(cfg.maxMessage)

		serve(r.Context(), h, cfg, conn, id, who)
	}
}

// roomFrom liest die Kennung aus /r/{room}.
func roomFrom(path string) (string, bool) {
	id := strings.TrimPrefix(path, "/r/")
	if len(id) != roomIDLength {
		return "", false
	}

	// Nur Kleinbuchstaben-Hex. Alles andere ist nicht von uns und wird nicht zu
	// einem Raumnamen gemacht - sonst waere jeder Pfad einer.
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}

	return id, true
}

func roleFrom(value string) (role, bool) {
	switch value {
	case string(roleHost):
		return roleHost, true
	case string(roleClient):
		return roleClient, true
	default:
		return "", false
	}
}

func serve(parent context.Context, h *hub, cfg config, conn *websocket.Conn, id string, who role) {
	self, other, err := h.join(id, who)
	if err != nil {
		refuse(parent, conn, err.Error())
		return
	}

	defer func() {
		if left := h.leave(id, self); left != nil {
			left.deliver(control(controlPeer(false)))
		}
		self.close()
	}()

	// Beide Seiten sofort auf denselben Stand bringen.
	if other != nil {
		self.deliver(control(controlPeer(true)))
		other.deliver(control(controlPeer(true)))
	} else {
		self.deliver(control(controlWaiting()))
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	go writeLoop(ctx, cancel, cfg, conn, self)

	readLoop(ctx, h, cfg, conn, id, self)
}

// readLoop nimmt Nachrichten entgegen und reicht Binaerframes weiter.
func readLoop(ctx context.Context, h *hub, cfg config, conn *websocket.Conn, id string, self *peer) {
	for {
		// Wer nichts sendet und auf keinen Ping antwortet, ist weg. Ohne diese
		// Grenze sammelten sich tote Verbindungen aus Mobilfunknetzen an, die
		// keinen Abschied schicken.
		ctxRead, cancelRead := context.WithTimeout(ctx, cfg.idleTimeout)
		kind, data, err := conn.Read(ctxRead)
		cancelRead()

		if err != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		}

		// Textframes gehoeren dem Relay und werden NIE weitergereicht. Dadurch kann
		// eine Nutzlast nie fuer eine Steuermeldung gehalten werden - der Frametyp
		// entscheidet, nicht der Inhalt.
		if kind != websocket.MessageBinary {
			continue
		}

		// Die Gegenseite jedes Mal frisch holen: Sie kann zwischen zwei Nachrichten
		// gekommen oder gegangen sein.
		other := currentPeer(h, id, self.role)
		if other == nil {
			continue
		}

		if !other.deliver(payload(data)) {
			// Die Gegenseite kommt nicht mit. Getrennt wird SIE, nicht wir -
			// sonst bestrafte die Ueberlastung den falschen von beiden.
			other.close()
		}
	}
}

// writeLoop schreibt, was fuer diese Verbindung eingereiht wurde, und haelt sie
// mit Pings am Leben.
func writeLoop(ctx context.Context, cancel context.CancelFunc, cfg config, conn *websocket.Conn, self *peer) {
	defer cancel()

	ticker := time.NewTicker(cfg.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-self.done:
			_ = conn.Close(websocket.StatusPolicyViolation, "too slow")
			return

		case message := <-self.send:
			kind := websocket.MessageBinary
			if message.control {
				kind = websocket.MessageText
			}

			ctxWrite, cancelWrite := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(ctxWrite, kind, message.data)
			cancelWrite()

			if err != nil {
				return
			}

		case <-ticker.C:
			ctxPing, cancelPing := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(ctxPing)
			cancelPing()

			if err != nil {
				return
			}
		}
	}
}

func currentPeer(h *hub, id string, who role) *peer {
	h.mu.Lock()
	defer h.mu.Unlock()

	r, ok := h.rooms[id]
	if !ok {
		return nil
	}

	return r.other(who)
}

func refuse(ctx context.Context, conn *websocket.Conn, why string) {
	ctxWrite, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_ = conn.Write(ctxWrite, websocket.MessageText, controlError(why))
	_ = conn.Close(websocket.StatusPolicyViolation, why)
}

// ---------------------------------------------------------------- Steuermeldungen

// Steuermeldungen werden als Text geschickt und tragen deshalb keine Verwechslungs-
// gefahr mit der Nutzlast. Von Hand zusammengesetzt statt ueber encoding/json: Es
// sind drei feste Formen, und der Inhalt stammt nie von aussen.

func controlWaiting() []byte { return []byte(`{"t":"waiting"}`) }

func controlPeer(up bool) []byte {
	if up {
		return []byte(`{"t":"peer","up":true}`)
	}
	return []byte(`{"t":"peer","up":false}`)
}

func controlError(why string) []byte {
	return []byte(`{"t":"error","why":"` + sanitize(why) + `"}`)
}

// sanitize haelt Anfuehrungszeichen und Steuerzeichen aus dem JSON heraus. Die
// Gruende kommen zwar alle aus diesem Programm, aber eine Meldung, die den
// umgebenden String sprengt, waere ein Fehler mit unnoetig langer Reichweite.
func sanitize(s string) string {
	var b strings.Builder

	for _, r := range s {
		if r < 0x20 || r == '"' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}

	return b.String()
}
