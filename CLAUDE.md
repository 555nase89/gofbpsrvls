# gofbpSRVLS

Web interfaces for goFBP Services (`../goFBP`), two programs:
- `cmd/gofbpsrvls`: local, no login (listens on 127.0.0.1 only); lists the Networks and Services
  connected to NATS, switches observing (trace) per Network and shows Events and log records live.
- `cmd/gofbpstore`: records log records and Events in SQLite and shows them with login, roles
  admin/viewer and user administration.

## Docs

- `docs/concept.md` (gofbpsrvls) and `docs/gofbpstore.md`: architecture, HTTP API, usage. Update
  them when behaviour or the API changes; keep the Network diagrams (`pkg/record/*.svg`,
  `pkg/users/*.svg`) in line with the `*_net.go` constructors.
- `tasks/todo.md`: done/open work; add a line under "Done" for each finished change.

## Rules

- Domain terms (Network, Service, Event, Observing, Run ID …) follow `../goFBP/docs/GLOSSARY.md`.
- Subjects and NATS types come from `gofbp/pkg/service` (`ObserveSubject`, `TraceSubject`,
  `LogSubject`, `LogHistorySubject`, `EventData`, `LogData`, `InportStats`, `ObserveStats`); never
  hard-code them here.
- The frontend in `pkg/webui/static` is shared by both programs: plain HTML/CSS/JS embedded via
  `go:embed`, no npm, no build step, no external scripts. Put untrusted data into the DOM with
  `textContent` only. It tells the programs apart by `GET api/me` (404 in gofbpsrvls).
- gofbpstore: everything that writes (recording, pruning, login, user changes) is a goFBP Network
  in `pkg/record` or `pkg/users`, run in-process with `pkg/flow.Runner`; reading stays plain Go on
  `pkg/store`. Never log the input of an Error Packet of `login`/`useradmin` (it holds passwords).
- Tests start an embedded nats-server and real goFBP Services (`gofbp/examples/text`) and use a
  SQLite file in `t.TempDir()`, so `/snap/go/current/bin/go test ./...` needs no external NATS.
- `github.com/555nase89/goFBP` is a regular dependency. The repository is private, so Go needs
  `GOPRIVATE=github.com/555nase89/*`.
