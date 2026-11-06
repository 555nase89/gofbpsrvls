# gofbpstore

gofbpstore records what goFBP Services publish on NATS in a SQLite file and shows it in the
browser with the same pages as gofbpsrvls, plus login and user administration. It is the
recording, multi-user sibling of the local `gofbpsrvls` (see [concept.md](concept.md)).

- **Log records** of every Service are always recorded, including the Log history a Service
  keeps from before gofbpstore started.
- **Events** are recorded only while observing (trace) is on for a Network. gofbpstore never
  switches it on by itself; an admin does in the UI (or anyone via the `observe` subject).
- Rows older than `retention` (default 30 days) are deleted every hour.

## Running

```sh
cd cmd/gofbpstore
cp example.json config.json                 # natscon, listen, db, retention, cookie_secure
/snap/go/current/bin/go build -o gofbpstore .
./gofbpstore -config config.json -adduser admin -role admin   # first admin; password from stdin
./gofbpstore -config config.json
```

| Config | Default | Meaning |
|--------|---------|---------|
| `natscon` | – | NATS connection, as in `gofbp/pkg/natscon` |
| `listen` | `127.0.0.1:8090` | HTTP address |
| `db` | `gofbpstore.db` | SQLite file (WAL mode: `-wal` and `-shm` files next to it) |
| `retention` | `720h` | how long Events and log records are kept (Go duration) |
| `cookie_secure` | `false` | mark the session cookie `Secure`; set it behind HTTPS |

gofbpstore speaks plain HTTP. To reach it from other machines, put it behind a reverse proxy with
TLS and set `cookie_secure`. Backup: copy the `db` file while gofbpstore is stopped (or use
`sqlite3 gofbpstore.db ".backup copy.db"`).

## Architecture

```
NATS ── record.Collector ──▶ Network record ──▶ SQLite ◀── storeweb (HTTP) ◀── browser
                              Network prune  ──▶   ▲
                              Networks login, useradmin ◀── storeweb
```

Everything that **writes** runs as a goFBP Network in-process (`pkg/flow.Runner`); reading
(queries, session check per request) is plain Go on `pkg/store`.

| Package | Role |
|---------|------|
| `pkg/store` | SQLite (`modernc.org/sqlite`, no CGO). Tables `events`, `logs` (`UNIQUE(service, seq)`, so a Log history recorded twice gives no duplicates; IDs of both use `AUTOINCREMENT` and are never reused after pruning, so streams reading `id > last` see every new row; `Open` rebuilds tables of older files that lack it), `users`, `sessions` (only the SHA-256 of a token is stored) |
| `pkg/flow` | `Runner`: an Instance of a Network with `Send` (fire and forget) and `Call` (wait for the first Packet of the Run, by Run ID; `ErrNoResult` if the Run ends without one). Error Packets nobody waits for are logged with their message only, never their input (it may hold a password) |
| `pkg/record` | Bricks and the Networks `record` and `prune`; `Collector`: every 10 s `discover.List`, per new Network a subscription on its Trace subject and on `LogSubject(base, "*")`, per new Service a request for its Log history (asked again on the next scan after a timeout; a Service without one is skipped); every message goes into the Network `record` |
| `pkg/users` | Bricks and the Networks `login` and `useradmin` (bcrypt, sessions of 12 h) |
| `pkg/storeweb` | HTTP server, see below |
| `pkg/webui` | Pages shared with gofbpsrvls |

### Networks

| Network | Diagram | Flow |
|---------|---------|------|
| `record` | [record.svg](../pkg/record/record.svg) | `In` (`{base, event}`) → `checkevent` → `storeevent`; `Log` (`{base, log}`) → `checklog` → `storelog` |
| `prune` | [prune.svg](../pkg/record/prune.svg) | `{before}` fans out to `pruneevents`, `prunelogs`, `prunesessions` |
| `login` | [login.svg](../pkg/users/login.svg) | `{name, password}` → `finduser` → `checkpassword` → `createsession` → `{token, expires, user}`. Unknown name and wrong password give the same error and take the same time (bcrypt against a dummy hash) |
| `useradmin` | [useradmin.svg](../pkg/users/useradmin.svg) | `{action, name, password, role}` → `routeaction` → `Create`: `checknewuser` → `hashnew` → `insertuser`; `Update`: `checkupdate` → `hashupdate` → `updateuser`; `Delete`: `deleteuser` |

Rules of `useradmin`: name not empty and without surrounding white space, password at least 10
characters, role `admin` or `viewer`; the last admin can be neither demoted nor deleted. `checkupdate` and
`deleteuser` check this early, and `pkg/store` checks it again in the same SQL statement as the
change (`store.ErrLastAdmin`), so concurrent changes cannot remove the last admin either. A new
password and deleting a user end that user's sessions.

## Roles

| | viewer | admin |
|---|---|---|
| See Networks, Services, Events, log records | ✓ | ✓ |
| Switch trace | | ✓ |
| Manage users | | ✓ |

## HTTP API

All `/api/*` routes except login and logout need the session cookie `gofbpstore_session`
(HttpOnly, SameSite=Strict); without it they answer 401, with the wrong role 403. Every POST must
send `Content-Type: application/json` (415 otherwise): together with SameSite=Strict this keeps
other sites from sending forged requests.

| Route | Role | Description |
|-------|------|-------------|
| `POST /api/login` | – | `{name, password}` → user, sets the cookie; 401 on wrong name or password |
| `POST /api/logout` | – | ends the session, clears the cookie |
| `GET /api/me` | any | the logged-in user. gofbpsrvls has no such route (404), which is how the shared pages tell the two apart |
| `GET /api/networks` | any | running Networks (discovery, `running: true`) and Networks only found in the store (`running: false`, no Services) |
| `POST /api/networks/observe` | admin | `{base, on}`, only for running Networks |
| `GET /api/networks/events?base=…` | any | Server-Sent Events: the newest 500 recorded Events, then every new row (the store is polled every second). Each row is `service.EventData` plus `id` and `base` |
| `GET /api/networks/events?base=…&before=<id>` | any | JSON array of the 500 Events before `id` ("Load older") |
| `GET /api/networks/logs?base=…[&before=<id>]` | any | the same for log records (`service.LogData` plus `id` and `base`) |
| `GET /api/users` | admin | all users (never the password hash) |
| `POST /api/users` | admin | a `useradmin` input; 400 with the rule's message if it fails |

## UI

The pages are those of gofbpsrvls (`pkg/webui`). With gofbpstore they additionally show:
- a login page, the user and role in the header and a Logout button;
- for admins a **Users** page (`#users`): change role, set a password, delete, create;
- Events also while trace is off (from the store) with a hint that none are recorded then;
- Networks without running Services greyed out with `stopped`, data from the store only;
- **Load older** below Events and Logs.
For viewers the Trace switch is disabled.
