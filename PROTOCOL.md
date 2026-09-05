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

Everything derived from it comes out of HKDF-SHA256, and only one of those outputs
ever reaches the relay. The `info` strings are given exactly, because a second
implementation has to arrive at the same bytes from this page alone:

| Derived | `info` | Salt | Length | Does the relay see it? |
|---|---|---|---|---|
| room id | `frameflip/v1/room` | empty | 16 bytes, written as 32 lowercase hex | **yes** — it needs it |
| `k_host` — encrypts PC → phone | `frameflip/v1/host` | `salt_host ‖ salt_client` | 32 bytes | no |
| `k_client` — encrypts phone → PC | `frameflip/v1/client` | `salt_host ‖ salt_client` | 32 bytes | no |

The room id is a one-way function of the secret. Knowing it lets you find the room;
it does not let you read anything in it. Separate keys per direction mean a captured
message cannot be replayed back the way it came.

The two salts are drawn fresh for each connection and exchanged in the open (§4), so
the message keys differ every session while the room id stays put. The room id has to
stay put — it is how the two sides find each other at all.

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

## 4. What goes inside the binary frames

The relay neither knows nor cares — but both ends have to agree exactly, so it is
written down here rather than in one of them.

### The handshake

Once the relay reports the other side is present, each end sends **one frame in the
clear**:

```
version (1 byte, currently 0x01) ‖ salt (16 bytes, random)
```

Both then derive the two message keys as in §1, with `salt_host ‖ salt_client` in that
order regardless of which side you are — otherwise the two ends compute different keys.

### Every frame after that

```
counter (8 bytes, big endian) ‖ tag (16 bytes) ‖ AES-256-GCM(payload)
```

with the nonce being four zero bytes followed by those same counter bytes, and the
counter bytes also passed as associated data. 24 bytes of overhead per message; the
counter starts at 0 for each direction of each connection.

A receiver accepts a frame only if the counter is **greater than** the highest it has
already accepted, and it advances that mark **only after** the tag verifies. Both
halves matter. Without the first, a captured frame can be played back inside the same
session. Without the second, anyone who can reach the room can send one frame with a
counter of 2⁶⁴−1 and lock the real peer out for good.

### Why the counter is the nonce

Because the key is different every session, and that is the only reason it is safe.

A fixed key with a counter that restarts at 0 is the textbook way to destroy AES-GCM:
same nonce, different plaintext, and the authentication key falls out. FrameFlip
restarting would have been exactly that — same pairing key, counter back to zero. The
per-session salts remove the possibility rather than making it unlikely, and they also
mean a recording from yesterday will not verify today.

### What this does not do

The handshake is **unauthenticated**. Someone who knows the room id can take a free
seat and send a salt. They still cannot read anything, and their frames fail the first
tag check — they can disrupt a pairing attempt, not listen in. Room ids are not
guessable in practice (128 bits), and the relay's two-connection limit bounds the
damage.

## 5. What the relay costs

Metrics for a running render are a few hundred bytes per second. A preview image is
requested on demand, in phone size and as JPEG — never the 72 MB PNG from the render
folder.

The relay holds no buffers of its own beyond one frame in flight per direction. It is
a pipe, and it is meant to stay one.
