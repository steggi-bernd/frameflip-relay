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
| `RELAY_MAX_ROOMS` | `128` | rooms held in memory, including rooms waiting for a peer |
| `RELAY_MAX_CONNECTIONS` | `256` | total open WebSocket connections |
| `RELAY_MAX_PER_IP` | `16` | simultaneous connections from one client address |
| `RELAY_TRUST_PROXY` | `false` | use `X-Forwarded-For`; set it only behind a trusted, sole reverse proxy |
| `RELAY_MESSAGES_PER_SECOND` | `64` | sustained inbound message rate per connection |
| `RELAY_MESSAGE_BURST` | `128` | short inbound message burst per connection |
| `RELAY_BYTES_PER_SECOND` | `4194304` | sustained inbound bandwidth per connection |
| `RELAY_BYTE_BURST` | `8388608` | short inbound bandwidth burst per connection |
| `RELAY_IDLE_SECONDS` | `90` | maximum time to wait for a ping response (minimum: 10 seconds) |
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
* **Room, connection and per-address limits**. Waiting rooms count too, so
  guessing random room ids cannot grow memory without bound.
* **Per-connection message and bandwidth limits**, with a small burst for normal
  transfers. Exceeding either disconnects only the sending connection.

The included Compose setup sets `RELAY_TRUST_PROXY=true` because it exposes no
relay port: Caddy is its sole path in and supplies the client address. Do not copy
that setting to a directly reachable relay.

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

### Checking a real installation

The tests above run against a server inside the same process. They say nothing about
the way in — certificate, reverse proxy, WebSocket upgrade over the real domain — and
that is where a deployment actually goes wrong.

```bash
go run ./probe wss://relay.example.org
```

It opens a room, plays both sides, and reports each step: the greeting, both
directions of forwarding, a payload beginning with `{`, 250 KB in one message, a
second host being refused, and the departure notice. It leaves nothing behind — the
room disappears with the connections.

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
