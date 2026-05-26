# Configuration Reference

trimon uses two separate config files with distinct security postures. This document describes the purpose of each file, how they are loaded, and what the `/config` endpoint exposes.

---

## Two Files, Two Audiences

### Probe config (`--probes`, required)

Contains the fields users might want to edit frequently: probe targets, intervals, labels, and global defaults. This file is safe to expose to unprivileged users — it's the data returned by `GET /config` and the only file that changes on hot-reload.

```yaml
global:
  probe_every: 30s
  packet_interval: 1s
  timeout: 5s
  count: 3
  source_ip: ""      # optional: bind to a specific local interface

probes:
  - name: ping-google-dns
    type: icmp
    target: dns.google
    probe_every: 10s  # overrides global
    timeout: 2s
    count: 5
    labels:
      site: primary
```

### Ops config (`--config`, required)

Contains deployment-specific settings owned by the platform operators: OTLP endpoints (which may carry credentials), TLS certificate paths, server bind address, and pipeline tuning. This file is read once at startup and never exposed over HTTP.

```yaml
exporters:
  stdout:
    enabled: true
    format: json

  otlp:
    enabled: true
    endpoint: "otelcol.internal:4317"
    protocol: grpc
    insecure: false
    tls:
      cert_file: /etc/trimon/tls/client.crt
      key_file:  /etc/trimon/tls/client.key
      ca_file:   /etc/trimon/tls/ca.crt

server:
  listen: "127.0.0.1:8080"

pipeline:
  buffer_size: 1000
```

Both flags are required. trimon exits with an error if either is missing.

---

## Loading

`config.Load(opsPath, probePath)` reads each file exactly once into a byte buffer before parsing. This prevents TOCTOU races — no second read of either file occurs anywhere in the load chain.

---

## Hot-Reload

`POST /reload` re-reads only the probe config file. The ops config is immutable after startup; changing exporters, TLS, or the server bind address requires a daemon restart.

This matches the convention of other infrastructure daemons (nginx, Prometheus): the config that controls network topology and credentials is load-once; the config that controls what to probe is live-reloadable.

The SHA256 fingerprint logged at reload is computed over the probe file bytes only, since that is the only file that changes.

---

## What `GET /config` Returns

The `/config` endpoint returns only the probe config — global defaults and the probes array — as JSON or YAML (negotiated via `Accept: application/x-yaml`):

```json
{
  "global": {
    "probe_every": "30s",
    "packet_interval": "1s",
    "timeout": "5s",
    "count": 3,
    "source_ip": ""
  },
  "probes": [
    {
      "name": "ping-google-dns",
      "type": "icmp",
      "target": "dns.google"
    }
  ]
}
```

The ops config is never exposed over HTTP. There is no `/ops-config` endpoint.
