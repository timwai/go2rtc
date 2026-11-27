# Xiaomi remote validation — P0-A

This branch is based on xiaomi-fix (e176e570e99622368f2a4de28443ea31b9b642bc).
The existing server/UI and default image are unchanged.

## Targets

| Model | Network | Channels |
| --- | --- | --- |
| isa.camera.cw501d | remote, no changes allowed | 2 |
| chuangmi.camera.ipc019 | remote, no changes allowed | 1 |
| chuangmi.camera.069a01 | remote, no changes allowed | 1 |
| chuangmi.camera.81ac1 | local | 1 |

## Run (Go version in go.mod)

```sh
git clone -b xiaomi-remote https://github.com/timwai/go2rtc.git
cd go2rtc
go test ./cmd/xiaomi-probe
go build -o xiaomi-probe ./cmd/xiaomi-probe
./xiaomi-probe -config /path/to/go2rtc.yaml -region cn
# Optional: IPC019 live HLS packet check; requires ffprobe
./xiaomi-probe -config /path/to/go2rtc.yaml -region cn -models chuangmi.camera.ipc019 -hls
```

Existing credentials are read from the YAML xiaomi section. If multiple valid
accounts exist, use -account with the local Xiaomi user ID. The program does
not write configuration, send device control actions by default, or print
IDs, IP addresses, token, keys, signed URLs, or raw cloud errors.
Device discovery now checks paginated account lists, the legacy list, and merged home lists (including shared homes when returned by Xiaomi). Secondary failures are printed without identifiers.
Do not share the YAML. The opt-in HLS action may open a session on the camera;
no stop-stream action is sent because it may affect another viewer. A signed
URL is passed locally to ffprobe and may be visible to local process inspection.
Run only on a trusted host and use cameras you are authorized to view.

## Docker build/run

Build the binary with a Go container, then run with the same read-only config
mount and network used by the existing go2rtc installation. The standalone
provisioning check needs only outbound HTTPS, not LAN discovery.

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.26 \
  go build -o xiaomi-probe ./cmd/xiaomi-probe
docker run --rm -v "$PWD/xiaomi-probe:/probe:ro" \
  -v /absolute/path/to/go2rtc.yaml:/config/go2rtc.yaml:ro \
  golang:1.26 /probe -config /config/go2rtc.yaml -region cn
```

## Interpretation and limitations

- response_received: cloud provisioning returned a vendor response, not a successful stream.
- cs2 with has_p2p_id and has_init_string: candidate for remote relay research.
- video_packets_received: ffprobe received video packets from the opt-in HLS stream.
- cloud_action_failed or no_http_stream_url: cloud HLS path did not work for this run.
- not_in_discovered_devices: not found through discovery; not a definitive offline status. Use -inventory to list model counts.

CS2 relay media transport after relay-ready, session linking, session keys retained for a real session, reconnect,
audio/video decoding, dual-channel playback, 24h recording, and other
models' HLS actions are NOT implemented here. The probe intentionally
discards generated private keys after provisioning. A separate relay driver
will need to retain a single key pair for both provisioning and MISS session.
HLS is implemented only for IPC019 from the published MIoT spec (SIID 4,
AIID 1, output PIID 5). Do not extrapolate those identifiers to other models.

References:
- https://github.com/eugeneRover/micam (protocol research only; no code copied)
- https://github.com/al-one/hass-xiaomi-miot/blob/master/custom_components/xiaomi_miot/camera.py
- https://home.miot-spec.com/spec/chuangmi.camera.ipc019
- https://home.miot-spec.com/spec/chuangmi.camera.069a01
- https://home.miot-spec.com/spec/isa.camera.cw501d

## P0-B1: relay negotiation (experimental)

```sh
./xiaomi-probe -config /path/to/go2rtc.yaml -region cn -inventory
./xiaomi-probe -config /path/to/go2rtc.yaml -region cn -models chuangmi.camera.ipc019,isa.camera.cw501d -relay
./xiaomi-probe -config /path/to/go2rtc.yaml -region cn -models chuangmi.camera.ipc019 -hls
```

Relay mode uses the cloud-provided CS2 rendezvous parameters, sends bounded UDP
requests, obtains a session port and waits for relay ready. It closes the socket
without starting video. It requires outbound UDP and can temporarily establish a
camera relay session. The tool reports only stages and counts, not endpoints or tokens.
Statuses: invalid_relay_parameters, udp_open_failed, no_relay_candidates,
relay_hello_timeout, relay_port_timeout, relay_join_timeout, relay_ready.
HLS response shape reports known keys and types only. A ready relay is not proof
of MISS authentication or playable video. No long-running connection or recorder yet.

## Shared camera discovery

The probe queries the account device list with explicit empty dids, shared homes,
and the device detail API for home and room DIDs. The optional -did flag resolves
an already authorized device by ID (for example from Home Assistant) and filters
normal probe output to that device. It does not bypass Xiaomi permissions. The
official HA OAuth session may have a different device scope from go2rtc's token.
A visible shared camera is not proof of MISS or HLS access. The owner account
is not required for discovery if the recipient has the relevant cloud permission.
Home-room pagination on the unmerged endpoint is not yet implemented.
