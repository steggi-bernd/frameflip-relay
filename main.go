// FrameFlip relay - bringt PC und Handy zusammen und reicht Bytes weiter.
//
// Mehr nicht. Er entschluesselt nichts, speichert nichts, deutet nichts. Sein
// gesamter Zustand ist "welche zwei Verbindungen gehoeren zusammen", und der stirbt
// mit den Verbindungen.
//
// Warum das reicht, steht in PROTOCOL.md: Der Kopplungsschluessel wird per QR-Code
// uebertragen und geht nie durchs Netz. Der Relay kennt nur die Raumkennung, eine
// Einwegableitung daraus - damit findet man den Raum, lesen kann man darin nichts.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type config struct {
	addr string

	// maxMessage begrenzt eine einzelne Nachricht. Vorschaubilder in Handygroesse
	// liegen bei 100-300 KB; ein Megabyte laesst Luft, ohne dass ein Raum als
	// Ablage taugt.
	maxMessage int64

	// sendQueue ist die Zahl der Nachrichten, die fuer eine langsame Gegenseite
	// zwischengehalten werden. Danach wird sie getrennt statt die schnelle Seite
	// auszubremsen.
	sendQueue int

	idleTimeout  time.Duration
	pingInterval time.Duration
}

func loadConfig() config {
	c := config{
		addr:         env("RELAY_ADDR", ":8080"),
		maxMessage:   int64(envInt("RELAY_MAX_MESSAGE", 1<<20)),
		sendQueue:    envInt("RELAY_SEND_QUEUE", 32),
		idleTimeout:  time.Duration(envInt("RELAY_IDLE_SECONDS", 90)) * time.Second,
		pingInterval: time.Duration(envInt("RELAY_PING_SECONDS", 25)) * time.Second,
	}

	// Ein Ping, der seltener kommt als der Timeout, waere eine Falle: Die
	// Verbindung faellt dann genau dadurch, dass sie ruhig ist.
	if c.pingInterval >= c.idleTimeout {
		c.pingInterval = c.idleTimeout / 3
	}

	return c
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}

	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("config: %s=%q ist unbrauchbar, nehme %d", key, v, fallback)
		return fallback
	}

	return n
}

func main() {
	cfg := loadConfig()
	h := newHub(cfg.sendQueue)

	mux := http.NewServeMux()
	mux.HandleFunc("/r/", newRelayHandler(h, cfg))

	// Fuer den Aussenmonitor. Nennt bewusst nur eine Zahl - wer wo verbunden ist,
	// geht niemanden etwas an, der diesen Pfad abruft.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"rooms":` + strconv.Itoa(h.count()) + `}`))
	})

	server := &http.Server{
		Addr:    cfg.addr,
		Handler: mux,

		// Kein WriteTimeout: Er wuerde eine WebSocket-Verbindung nach Ablauf hart
		// kappen, egal wie lebendig sie ist. Die Zeitgrenzen fuer offene
		// Verbindungen setzt der Handler selbst.
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("relay lauscht auf %s", cfg.addr)

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("relay: %v", err)
		}
	}()

	// Sauber beenden, damit ein Neustart des Containers keine halben Verbindungen
	// hinterlaesst.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Print("relay wird beendet")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = server.Shutdown(ctx)
}
