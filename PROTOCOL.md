# FrameFlip relay — protocol

The relay exists for one reason: a phone on a mobile network cannot reach a PC behind
a home router. It brings the two together and forwards bytes between them.

It does **nothing else**. It does not decrypt, does not store, does not interpret. Its
entire state is "which two connections belong together", and that state dies with the
connections.

---

## 1. Why the relay learns nothing

The pairing secret is a random 256-bit key that FrameFlip shows as a QR code. It
**never crosses the network** — the phone reads it off the screen.

Everything derived from it comes out of one HKDF, and only one of those outputs ever
reaches the relay:

| Derived | Purpose | Does the relay see it? |
|---|---|---|
| `room = HKDF(key, "room")` | which two connections belong together | **yes** — it needs it |
| `k_host = HKDF(key, "host")` | encrypts PC → phone | no |
| `k_client = HKDF(key, "client")` | encrypts phone → PC | no |

The room id is a one-way function of the secret. Knowing it lets you find the room;
it does not let you read anything in it. Separate keys per direction mean a captured
message cannot be replayed back the way it came.

That is why the relay can be a dumb pipe, and why it does not need to be trusted.

## 2. What the relay does verify

Nothing about identity — it cannot, without the key. What it enforces is house rules,
so a stranger who guesses a room id can be a nuisance but not a threat:

* **Two connections per room.** One host, one client. A third is refused.
* **One host per room.** A second host attempt is refused rather than taking over —
  otherwise anyone knowing the room id could displace the real PC.
* **Message size limit.** Anything larger is refused and the connection closed.
* **Rate limit per connection**, so a room cannot be used to flood the other side.
* **Idle timeout.** A connection that neither sends nor answers a ping is dropped.

An impostor who joins a room still cannot produce a message the other side accepts:
AES-GCM verifies the authentication tag before anything is interpreted, and a wrong
key fails that check. The impostor's noise is discarded one layer above the relay.

## 3. Wire format

WebSocket, one connection per participant.

```
GET /r/{room}?role=host      Host   (FrameFlip on the PC)
GET /r/{room}?role=client    Client (the phone)
```

`room` is 32 lowercase hex characters — the first 16 bytes of the HKDF output. Longer
would be pointless: it is not a secret, only a name.

**Binary frames are forwarded verbatim** to the other side of the room, and never to
anyone else. The relay does not look inside them.

**Text frames are the relay's own protocol** and are never forwarded:

| From the relay | Meaning |
|---|---|
| `{"t":"waiting"}` | you are in, the other side is not here yet |
| `{"t":"peer","up":true}` | the other side has arrived |
| `{"t":"peer","up":false}` | the other side is gone |
| `{"t":"error","why":"..."}` | refused; the connection closes right after |

Splitting it this way means the relay never has to parse a payload to decide what to
do with it — the frame type already says so. A payload that looks like a control
message cannot be mistaken for one.

### A note for whoever writes a client

Raise your library's read limit to match `RELAY_MAX_MESSAGE` (1 MiB by default).
Several WebSocket libraries default to 32 KB — Go's `coder/websocket` among them —
which is enough for every metrics message and too small for the first preview image.
The failure is unhelpful when it arrives: the transfer dies with a "message too big"
raised by *your own* reader, and the relay looks like the culprit.

This is not hypothetical. It is how the first end-to-end test against the live server
failed.

## 4. What goes inside the encrypted frames

That is between FrameFlip and the app; the relay neither knows nor cares. For
completeness:

```
nonce (12 bytes) ‖ AES-256-GCM(counter ‖ payload)
```

The counter runs per direction and rejects anything not strictly increasing — a
recorded message cannot be played back later.

## 5. What the relay costs

Metrics for a running render are a few hundred bytes per second. A preview image is
requested on demand, in phone size and as JPEG — never the 72 MB PNG from the render
folder.

The relay holds no buffers of its own beyond one frame in flight per direction. It is
a pipe, and it is meant to stay one.
