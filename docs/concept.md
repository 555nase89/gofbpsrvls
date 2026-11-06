# gofbpSRVLS

gofbpSRVLS is a local web interface for goFBP Services. It finds every Service connected to NATS,
groups them by Network, switches observing (trace) on and off and shows the Events of an observed
Network and the log records of its Services live in the browser. There is no login: the server listens on `127.0.0.1:8080` by default
and must not be exposed.

For recording into a database, with login and user administration, see
[gofbpstore.md](gofbpstore.md); both programs share the pages in `pkg/webui`.

## Running

```sh
cd cmd/gofbpsrvls
cp example.json config.json        # adjust url / nkeyfile
/snap/go/current/bin/go build -o gofbpsrvls .
./gofbpsrvls -config config.json      # -listen 127.0.0.1:9090 overrides "listen"
```

Open http://127.0.0.1:8080. The list refreshes every 5 s.

## How it works

| Need | goFBP mechanism | Package |
|------|-----------------|---------|
| Find Services | One `$SRV.STATS` request, replies collected for 500 ms; replies with metadata `base` and `network` are goFBP Services and are grouped by `base` | `pkg/discover` |
| Observing state per Service | `data` of the `observe` endpoint in `$SRV.STATS` (`service.ObserveStats`) | `pkg/discover` |
| Switch trace | Publish `true`/`false` on `service.ObserveSubject(base)`. There is no queue group, so **all Services of the Network** switch | `pkg/web` |
| Receive Events | Subscribe to `service.TraceSubject(base)` (`service.EventData` JSON) | `pkg/trace` |
| Receive log records | Subscribe to `service.LogSubject(base, "*")` (`service.LogData` JSON, all Services of the Network), then request `service.LogHistorySubject(base, id)` of every Service | `pkg/trace`, `pkg/web` |

`pkg/trace.Hub` holds one NATS subscription per subject (Trace subject, Log subjects of a Network)
while at least one browser watches it and fans the messages out to all of them. It keeps the last
500 messages per subject, which a newly opened browser gets first. A browser that falls behind by more than 256 Events loses Events instead of
slowing down the others. The browser itself keeps at most 2000 Events.

For logs, the server subscribes first and then asks every Service for its Log history (the last
500 records by default, kept by the Service itself). So the browser also gets records written
before gofbpSRVLS started, e.g. `textservice started`. The history is sent sorted by time; live
records whose `seq` was already sent for that Service are skipped. A Service built with an older
goFBP has no Log history and shows live records only, if any. Records written before
`service.Start` (config, NATS connect) are only on the Service's stderr.

Services that start later than the switch are not observing (observing is off after `Start`); the
UI then shows "Services differ". Switching trace on again reaches all of them.

## HTTP API

| Route | Description |
|-------|-------------|
| `GET /` | Embedded UI (`pkg/webui/static`) |
| `GET /api/networks` | JSON list of `discover.Network` with its `services` (ID, version, start time, `observing`, received, errors, dropped, Packets per Outport) |
| `POST /api/networks/observe` | Body `{"base":"fbp.…","on":true}`; 204 on success, 404 if no Service with this base subject runs |
| `GET /api/networks/events?base=fbp.…` | Server-Sent Events; each `data:` line is one `service.EventData` |
| `GET /api/networks/logs?base=fbp.…` | Server-Sent Events; each `data:` line is one `service.LogData`: first the Log history of all Services by time, then live records |

All routes taking a base accept only base subjects of Networks that are currently running, so the API
cannot publish on arbitrary subjects.

## UI

- Left: all Networks with package path; a green `trace` badge when observing is on.
- Right: the selected Network with its Inports and Outports, a table of its Services and the
  Trace switch.
- Tabs **Events** and **Logs** below the Services; the tab is kept in the URL (`#logs`).
- Events, with trace on: newest first (time, Service, kind, Run ID, Packet ID ← parent ID,
  node, port, data or error). Error Events are highlighted. The filter matches Run ID, node, port,
  kind, Service, error and data; clicking a Run ID filters by it. Pause stops redrawing, Clear empties
  the list.
- Logs, always (no switch): time, Service, level (Warn yellow, Error red), message and
  attributes as `key=value`, newest first. A level selection hides lower levels (default Info),
  the filter matches Service ID, message and attributes; clicking a Service ID filters by it.
  The browser keeps at most 2000 records. Pause and Clear work as for Events.
