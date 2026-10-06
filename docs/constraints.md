# Locked owner constraints

This file is the single current source of the locked owner constraints.

## Fork scope

- The fork is server-only.
- The trimmed server keeps VLESS, REALITY, Vision, TCP, UDP, and freedom.
- The stock client stays unchanged.
- The config schema and the REALITY handshake stay compatible with the stock
  client.

## Client

- The stock Hiddify client runs on Android, Windows, and Linux.
- TUN mode is always on. There is no bypass rule.
- One shared UUID serves at most ten devices.
- The inbound listens on IPv4 only, on port 443.
- The outbound is dual-stack: IPv4 and IPv6.

## Destination

- `dest` and SNI use `whatsapp.net` only.
- No destination or SNI subdomain substitution is allowed.
- There is no fallback. If the destination fails, the connection stops.
  The owner accepts this risk.

## Operations

- No live maintenance, reboot, deployment, tuning, or production change without
  owner approval.
- APT updates are manual only. Automatic updates are off.
- Automatic reboot is off. A reboot needs owner approval.
- `maintain.sh` asks for approval at each step. It never runs automatically.
- CI runs offline tests only. CI does not connect to the production server.

## Verification state

- This file does not verify any current production state.
