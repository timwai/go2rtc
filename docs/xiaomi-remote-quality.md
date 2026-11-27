# C700 and CW501D diagnostic update
CW501D defaults to native quality 3 instead of 2, including both lenses.
Explicit subtype values still override the default. This is based on a reported
working CW501D configuration in upstream issue #2234, not a measured resolution.
Existing sources with subtype=2 must be updated manually to subtype=3.
The probe supports -quality 0..3 (default 2 for comparison).
C700: test LAN auto/TCP/UDP independently and remote media with quality 2 and 3.
For a source with no sound, test audio=0 to isolate media negotiation.
The LAN IP comparison now uses net.IP.Equal, avoiding IPv4 representation
mismatch. This is the relevant upstream handshake fix; it does not guarantee
that the camera responds to unicast discovery.
The producer now drains unsolicited post-auth commands and reports distinct
connection/authentication and media-start failures. No C700 fix is confirmed
until real-device tests pass. See upstream issues #2506, #2048 and #2393.
