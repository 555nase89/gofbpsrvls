# gofbpSRVLS

Web interfaces for [goFBP](https://github.com/555nase89/goFBP) Services. Two programs share the
same browser UI:

| Program | Purpose | Default address |
|---------|---------|-----------------|
| `gofbpsrvls` | Local live view, no login. Lists the Networks and Services connected to NATS, switches observing (trace) per Network and shows Events and log records live. | `127.0.0.1:8080` |
| `gofbpstore` | Records log records and Events in SQLite and shows them with login, roles `admin`/`viewer` and user administration. | `127.0.0.1:8090` |

## Requirements

- Go (see `go.mod`)
- A running NATS server with goFBP Services connected to it
- Access to the private goFBP repository:

  ```sh
  export GOPRIVATE=github.com/555nase89/*
  ```

## gofbpsrvls

```sh
cd cmd/gofbpsrvls
cp example.json config.json        # adjust natscon.url / natscon.nkeyfile
go build -o gofbpsrvls .
./gofbpsrvls -config config.json   # -listen 127.0.0.1:9090 overrides "listen"
```

Open http://127.0.0.1:8080. The Network list refreshes every 5 s.

gofbpsrvls has no login and must not be exposed beyond `127.0.0.1`.

## gofbpstore

```sh
cd cmd/gofbpstore
cp example.json config.json
go build -o gofbpstore .
./gofbpstore -config config.json -adduser admin -role admin   # first admin, password from stdin
./gofbpstore -config config.json
```

| Config | Default | Meaning |
|--------|---------|---------|
| `natscon` | – | NATS connection (`url`, `nkeyfile`) |
| `listen` | `127.0.0.1:8090` | HTTP address |
| `db` | `gofbpstore.db` | SQLite file |
| `retention` | `720h` | how long Events and log records are kept (Go duration) |
| `cookie_secure` | `false` | mark the session cookie `Secure`; set it behind HTTPS |

Log records are always recorded; Events only while observing is on for a Network. gofbpstore
speaks plain HTTP — to reach it from other machines, put it behind a reverse proxy with TLS and set
`cookie_secure`.

## Layout

| Path | Contents |
|------|----------|
| `cmd/gofbpsrvls`, `cmd/gofbpstore` | Executables (`main.go`, `example.json`) |
| `pkg/discover` | Finds goFBP Services via `$SRV.STATS` and groups them by Network |
| `pkg/trace` | Fans out Trace and Log subscriptions to browsers |
| `pkg/web` | HTTP server of gofbpsrvls |
| `pkg/webui` | Shared UI (plain HTML/CSS/JS, embedded, no build step) |
| `pkg/store` | SQLite storage of gofbpstore |
| `pkg/record`, `pkg/users` | goFBP Networks for recording/pruning and login/password change/user administration |
| `pkg/flow` | Runs a goFBP Network in-process |
| `pkg/storeweb` | HTTP server of gofbpstore |
| `docs/` | Concept and operations documentation |
| `tasks/` | Open and finished work |

## Tests

```sh
go test ./...
```

The tests start an embedded nats-server and real goFBP Services, so no external NATS is needed.

## Documentation

- [docs/concept.md](docs/concept.md) — gofbpsrvls: how it works, HTTP API, UI
- [docs/gofbpstore.md](docs/gofbpstore.md) — gofbpstore: architecture, Networks, roles, HTTP API

## License

BSD 3-Clause, see [LICENSE](LICENSE).
