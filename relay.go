package main

import (
	"context"
	"net"
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

func newRelayHandler(h *hub, cfg config, admission *admission) http.HandlerFunc {
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

		ip := clientIP(r, cfg.trustProxy)
		if err := admission.acquire(ip); err != nil {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		defer admission.release(ip)

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

// clientIP wertet Forwarded-Header ausschliesslich aus, wenn der Betreiber den
// einzigen Zugang ueber einen vertrauten Reverse Proxy garantiert. Andernfalls
// ist RemoteAddr die einzige Adresse, die ein Client nicht faelschen kann.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
		if candidate := strings.TrimSpace(first); net.ParseIP(candidate) != nil {
			return candidate
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}

	return r.RemoteAddr
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
	limiter := newInboundLimiter(cfg)

	for {
		// KEINE Frist auf das Lesen.
		//
		// Hier stand eine, begruendet mit "wer nichts sendet und auf keinen Ping
		// antwortet, ist weg". Der Code machte daraus etwas anderes: Read kehrt nur
		// bei einer Datennachricht zurueck, Pongs setzen die Frist nicht zurueck.
		// Wer also nur zuhoert - und genau das tut ein Handy, das Renderfortschritt
		// anzeigt - flog alle 90 Sekunden raus. In der App blinkte regelmaessig
		// "verbinde" auf, ohne dass jemand etwas getan haette.
		//
		// Auf Lebendigkeit prueft ohnehin writeLoop: Es pingt im festen Takt und
		// wartet auf die Antwort. Bleibt sie aus, endet die Schleife, cancel()
		// greift, und dieses Read bricht mit ab. Die Frist war also nicht nur
		// falsch, sondern auch ueberfluessig.
		kind, data, err := conn.Read(ctx)

		if err != nil {
			// Close kann auf die Close-Antwort eines Clients warten. Diese Schleife
			// besitzt aber den Raumslot; ein Client, der gar nicht mehr liest,
			// duerfte dadurch weder diesen Slot noch seinen Peer festhalten.
			conn.CloseNow()
			return
		}

		// Textframes gehoeren dem Relay und werden NIE weitergereicht. Dadurch kann
		// eine Nutzlast nie fuer eine Steuermeldung gehalten werden - der Frametyp
		// entscheidet, nicht der Inhalt.
		if kind != websocket.MessageBinary {
			continue
		}

		if !limiter.allow(len(data)) {
			// Wie oben: Die Abmeldung des Peers muss sofort erfolgen. Der Rate-Limit-
			// Verstoss wartet nicht auf einen Close-Handshake.
			conn.CloseNow()
			return
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
			conn.CloseNow()
			return

		case message := <-self.send:
			kind := websocket.MessageBinary
			if message.control {
				kind = websocket.MessageText
			}

			ctxWrite, cancelWrite := context.WithTimeout(ctx, cfg.idleTimeout)
			err := conn.Write(ctxWrite, kind, message.data)
			cancelWrite()

			if err != nil {
				return
			}

		case <-ticker.C:
			ctxPing, cancelPing := context.WithTimeout(ctx, pingTimeout(cfg))
			err := conn.Ping(ctxPing)
			cancelPing()

			if err != nil {
				return
			}
		}
	}
}

func pingTimeout(cfg config) time.Duration {
	// coder/websocket verarbeitet Ping/Pong nur waehrend die Gegenseite liest.
	// Eine App, die gerade nur wartet, ist trotzdem ein gueltiger Client. Zehn
	// Sekunden sind die untere Schranke, damit ein kurz pausierter Reader nicht
	// wie eine tote Verbindung behandelt wird; die normale Vorgabe von 90 Sekunden
	// bleibt unveraendert wirksam.
	return max(cfg.idleTimeout, 10*time.Second)
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
	conn.CloseNow()
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
