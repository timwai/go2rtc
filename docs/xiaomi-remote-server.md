# Native CS2 WAN preview

This experimental branch integrates the successful short native media probe into
go2rtc's existing MISS producer. It is opt-in: existing LAN URLs are unchanged.
The server's Xiaomi discovery now also queries shared devices and homes.
Add an entry marked 公网中继 from the Xiaomi source page, or append
`&transport=relay` to an existing authorized Xiaomi source URL.
For CW501D use channel=0 (first lens) and channel=1 (second lens), the
existing go2rtc channel numbering, not the probe's 1/2 numbering.
Two simultaneous lens sessions have not yet been verified.
Remote two-way audio is intentionally not advertised. The existing LAN path remains unchanged.
The relay uses outbound UDP and no remote router changes. Handshake retries
fresh sessions up to three times with modest packet pacing.
A server-side consumer must reconnect after session failure. Long-running
recording, automatic LAN/WAN fallback and new UI are not yet implemented.
Do not expose an unauthenticated go2rtc API to the Internet.

The CI builds standalone Linux binaries and publishes only the separate tag
ghcr.io/timwai/go2rtc:xiaomi-remote if the test/build jobs succeed.
The latest and xiaomi-fix tags are untouched.
