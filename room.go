package main

import (
	"errors"
	"sync"
)

// Rolle einer Verbindung. Genau zwei je Raum, und jede nur einmal.
type role string

const (
	roleHost   role = "host"   // FrameFlip auf dem PC
	roleClient role = "client" // das Handy
)

var (
	errRoomFull  = errors.New("room is full")
	errRoleTaken = errors.New("role already taken")
)

// outgoing traegt den Frametyp MIT, statt ihn am Inhalt zu erraten.
//
// Der naheliegende Weg - beim Schreiben nachsehen, ob die Nachricht mit '{'
// beginnt - waere hier falsch: Nutzlast faengt mit einem zufaelligen Nonce an, und
// dessen erstes Byte ist in einem von 256 Faellen 0x7B. Jedes 256. Paket ginge
// dadurch als Textframe hinaus und kaeme drueben als Steuermeldung an. Wer den
// Typ kennt, soll ihn mitgeben.
type outgoing struct {
	control bool
	data    []byte
}

// peer ist eine Verbindung samt eigener Sendewarteschlange.
//
// Die Warteschlange ist nicht Zierde: Ohne sie schriebe die Leseschleife der einen
// Seite direkt in den Socket der anderen. Ein langsames Handy im Mobilfunknetz
// wuerde damit den PC ausbremsen, obwohl der nichts dafuer kann. Ist die
// Warteschlange voll, wird die langsame Seite getrennt - nicht die schnelle.
type peer struct {
	role role
	send chan outgoing
	done chan struct{}

	once sync.Once
}

func newPeer(r role, queue int) *peer {
	return &peer{
		role: r,
		send: make(chan outgoing, queue),
		done: make(chan struct{}),
	}
}

// close ist mehrfach aufrufbar - beide Schleifen einer Verbindung duerfen es tun.
func (p *peer) close() {
	p.once.Do(func() { close(p.done) })
}

// deliver stellt zu, ohne je zu blockieren.
//
// Rueckgabe false heisst: Der Empfaenger kommt nicht mit. Der Aufrufer trennt ihn
// dann. Zu warten waere die schlechtere Wahl - es hielte die Gegenseite auf.
func (p *peer) deliver(message outgoing) bool {
	select {
	case p.send <- message:
		return true
	case <-p.done:
		return false
	default:
		return false
	}
}

// payload reicht eine fremde Nachricht weiter, control eine eigene.
func payload(data []byte) outgoing { return outgoing{data: data} }
func control(data []byte) outgoing { return outgoing{control: true, data: data} }

// room haelt hoechstens zwei Verbindungen zusammen.
type room struct {
	host   *peer
	client *peer
}

func (r *room) empty() bool { return r.host == nil && r.client == nil }

func (r *room) slot(who role) **peer {
	if who == roleHost {
		return &r.host
	}
	return &r.client
}

func (r *room) other(who role) *peer {
	if who == roleHost {
		return r.client
	}
	return r.host
}

// hub verwaltet alle Raeume. Der gesamte Zustand des Dienstes steckt hier drin -
// und verschwindet mit der letzten Verbindung wieder.
type hub struct {
	mu    sync.Mutex
	rooms map[string]*room
	queue int
}

func newHub(queue int) *hub {
	return &hub{rooms: make(map[string]*room), queue: queue}
}

// join nimmt eine Verbindung auf. Der zweite Rueckgabewert ist die Gegenseite,
// sofern sie bereits da ist.
func (h *hub) join(id string, who role) (*peer, *peer, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	r, ok := h.rooms[id]
	if !ok {
		r = &room{}
		h.rooms[id] = r
	}

	slot := r.slot(who)

	// Belegte Rolle wird NICHT uebernommen, sondern abgewiesen.
	//
	// Andersherum koennte jeder, der die Raumkennung kennt, den echten PC
	// verdraengen - und die Raumkennung ist kein Geheimnis, sie ist nur ein Name.
	if *slot != nil {
		if r.empty() {
			delete(h.rooms, id)
		}
		return nil, nil, errRoleTaken
	}

	p := newPeer(who, h.queue)
	*slot = p

	return p, r.other(who), nil
}

// leave meldet ab und gibt die Gegenseite zurueck, damit sie benachrichtigt wird.
func (h *hub) leave(id string, p *peer) *peer {
	h.mu.Lock()
	defer h.mu.Unlock()

	r, ok := h.rooms[id]
	if !ok {
		return nil
	}

	slot := r.slot(p.role)
	if *slot != p {
		// Bereits durch eine andere Abmeldung ersetzt - nichts zu tun.
		return nil
	}

	*slot = nil
	other := r.other(p.role)

	if r.empty() {
		delete(h.rooms, id)
	}

	return other
}

// rooms zaehlt die belegten Raeume. Nur fuer die Statusabfrage.
func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rooms)
}
