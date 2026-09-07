package main

import (
	"sync"
	"time"
)

// admission begrenzt die Zahl offener WebSocket-Verbindungen global und pro
// Clientadresse. Der Zaehler umfasst auch Verbindungen, die noch auf die
// Gegenseite warten: Gerade solche leeren Raeume waeren sonst billig zu fluten.
type admission struct {
	mu             sync.Mutex
	active         int
	byIP           map[string]int
	maxConnections int
	maxPerIP       int
}

func newAdmission(maxConnections, maxPerIP int) *admission {
	return &admission{
		byIP:           make(map[string]int),
		maxConnections: maxConnections,
		maxPerIP:       maxPerIP,
	}
}

func (a *admission) acquire(ip string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.active >= a.maxConnections {
		return errTooManyPeers
	}
	if a.byIP[ip] >= a.maxPerIP {
		return errTooManyFromIP
	}

	a.active++
	a.byIP[ip]++
	return nil
}

func (a *admission) release(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.active > 0 {
		a.active--
	}
	if a.byIP[ip] <= 1 {
		delete(a.byIP, ip)
		return
	}
	a.byIP[ip]--
}

// inboundLimiter ist ein Token-Bucket ohne Hintergrund-Goroutine. Jede
// Verbindung hat ihren eigenen; nach einer Ruhephase darf sie bis zum Burst
// senden, aber nie dauerhaft mehr als die konfigurierte Rate.
type inboundLimiter struct {
	messageTokens float64
	byteTokens    float64
	messageRate   float64
	byteRate      float64
	messageBurst  float64
	byteBurst     float64
	last          time.Time
}

func newInboundLimiter(cfg config) *inboundLimiter {
	return &inboundLimiter{
		messageTokens: float64(cfg.messageBurst),
		byteTokens:    float64(cfg.byteBurst),
		messageRate:   float64(cfg.messagesPerSecond),
		byteRate:      float64(cfg.bytesPerSecond),
		messageBurst:  float64(cfg.messageBurst),
		byteBurst:     float64(cfg.byteBurst),
		last:          time.Now(),
	}
}

func (l *inboundLimiter) allow(bytes int) bool {
	now := time.Now()
	elapsed := now.Sub(l.last).Seconds()
	l.last = now

	l.messageTokens = min(l.messageBurst, l.messageTokens+elapsed*l.messageRate)
	l.byteTokens = min(l.byteBurst, l.byteTokens+elapsed*l.byteRate)

	if l.messageTokens < 1 || l.byteTokens < float64(bytes) {
		return false
	}

	l.messageTokens--
	l.byteTokens -= float64(bytes)
	return true
}
