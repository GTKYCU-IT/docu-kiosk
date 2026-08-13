# Hand-maintained typed wire protocol

Kiosk↔broker WebSocket messages are a sealed set of typed messages in Go (the hub's wire shapes, marshaled through a single path) mirrored by hand in the kiosk SPA (`web/src/lib/protocol.ts`) rather than generated from a shared schema.

The set is bidirectional: the broker writes the `connected` greeting when a kiosk connects and pushes `sign` instructions on demand; the kiosk replies with `status` frames (`ready` or `signing`) — its first client frame after receiving the greeting, and again after every ready/signing transition, including reporting `signing` when a reconnect lands during an active signing session. The broker gates session publication on that initial status frame, and status is owned by the current per-identity generation: reports from replaced, disconnected, or deleted generations are ignored.

We hand-maintain the mirror because the message set is small with low churn. Code generation would add a build step and toolchain for little leverage at this size. The cost — the protocol shape must be edited in Go and in the SPA in lockstep — is accepted, and any change to the message set is the one place both sides must agree.
