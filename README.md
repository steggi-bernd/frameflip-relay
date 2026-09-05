# FrameFlip relay

**Brings a PC and a phone together and forwards bytes between them.**

That is the whole job. A phone on a mobile network cannot reach a PC behind a home
router; this sits in the middle and passes packets along.

It does not decrypt, does not store, does not interpret. Its entire state is "which
two connections belong together", and that dies with the connections.

Part of [FrameFlip](https://github.com/steggi-bernd/FrameFlip).

## Why it does not need to be trusted

The pairing secret is a random 256-bit key that FrameFlip shows as a QR code. It
**never crosses the network** — the phone reads it off the screen.

The relay only ever learns the *room id*, a one-way derivation of that secret.
Knowing it lets you find the room; it does not let you read anything in it. The
payload is AES-256-GCM with keys the relay never sees.

So an operator of this service — including you — cannot read what passes through it.
That is deliberate, and it is why the relay can be this small.

Details in [PROTOCOL.md](PROTOCOL.md).

## Running it

```bash
docker compose up -d --build
```

It publishes no port. Reachability comes from the reverse proxy in front of it,
which also provides TLS. For Caddy:

```caddy
relay.example.org {
    reverse_proxy frameflip-relay:8080
}
```

WebSocket upgrades need no special handling in Caddy 2 — it forwards them as they
come.

### Settings

| Variable | Default | Meaning |
|---|---|---|
| `RELAY_ADDR` | `:8080` | listening address |
| `RELAY_MAX_MESSAGE` | `1048576` | largest single message, in bytes |
| `RELAY_SEND_QUEUE` | `32` | messages held for a slow peer before it is dropped |
| `RELAY_IDLE_SECONDS` | `90` | a connection that neither sends nor answers a ping is dropped |
| `RELAY_PING_SECONDS` | `25` | keep-alive interval |

`GET /health` answers `{"ok":true,"rooms":N}` — a count and nothing else. Who is
connected where is nobody's business who can reach that path.

## House rules

The relay cannot verify identity — it has no key. What it enforces are limits, so
that someone who guesses a room id can be a nuisance but not a threat:

* **Two connections per room**, one host and one client. A third is refused.
* **A taken role is refused, not taken over.** Otherwise anyone knowing the room id
  could displace the real PC — and the room id is not a secret, it is a name.
* **Message size limit**, **send queue limit**, **idle timeout**.

An impostor who joins a room still cannot produce a message the other side accepts:
AES-GCM checks the authentication tag before anything is interpreted, and a wrong key
fails that check.

## Tests

```bash
go test ./...
```

Six tests over real WebSocket connections against a real server — forwarding both
ways, refusing a second host, notifying on departure and cleaning up the room, text
frames never being forwarded, address validation, and that an error message cannot
break out of its JSON string.

One of them is worth naming: it forwards a payload that *begins with* `{`. An earlier
version decided the frame type by looking at the first byte, which would have sent
roughly one message in 256 as a text frame — where the other side would have read it
as a control message. The frame type now travels with the message instead of being
guessed.

Race detection needs cgo and is therefore not run on Windows; the Docker build runs
`go vet` and the tests on Linux before it produces a binary.

## What it costs

A static binary of about 6 MB in an empty image — no shell, no package manager, no
operating system underneath. A publicly reachable service with no runtime has nothing
that needs patching and nothing to look around in after a break-in.

Metrics for a running render are a few hundred bytes per second. A preview image is
requested on demand, at phone size and as JPEG.

## Licence

MIT, like FrameFlip.
