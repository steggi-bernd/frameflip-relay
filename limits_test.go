package main

import (
	"net/http/httptest"
	"testing"
)

func TestAdmissionPerIP(t *testing.T) {
	a := newAdmission(3, 1)
	if err := a.acquire("203.0.113.42"); err != nil {
		t.Fatalf("erste Verbindung: %v", err)
	}
	if err := a.acquire("203.0.113.42"); err != errTooManyFromIP {
		t.Fatalf("gleiche Adresse: %v", err)
	}
	if err := a.acquire("203.0.113.43"); err != nil {
		t.Fatalf("andere Adresse: %v", err)
	}
	a.release("203.0.113.42")
	if err := a.acquire("203.0.113.42"); err != nil {
		t.Fatalf("Adresse wurde nach Trennung nicht freigegeben: %v", err)
	}
}

func TestClientIPTrustsProxyOnlyWhenConfigured(t *testing.T) {
	r := httptest.NewRequest("GET", "http://relay.example/r/room", nil)
	r.RemoteAddr = "198.51.100.10:4321"
	r.Header.Set("X-Forwarded-For", "203.0.113.8, 198.51.100.1")

	if got := clientIP(r, false); got != "198.51.100.10" {
		t.Fatalf("ohne Proxy-Vertrauen: %q", got)
	}
	if got := clientIP(r, true); got != "203.0.113.8" {
		t.Fatalf("mit Proxy-Vertrauen: %q", got)
	}
}

func TestInboundLimiter(t *testing.T) {
	cfg := testConfig()
	cfg.messagesPerSecond = 1
	cfg.messageBurst = 1
	cfg.bytesPerSecond = 10
	cfg.byteBurst = 10

	l := newInboundLimiter(cfg)
	if !l.allow(10) {
		t.Fatal("der konfigurierte Burst muss erlaubt sein")
	}
	if l.allow(1) {
		t.Fatal("ein zweites Paket ausserhalb des Bursts muss abgewiesen werden")
	}
}
