# gofbpSRVLS – tasks

## Done
- Project set up (`cmd/gofbpsrvls`, `pkg/discover`, `pkg/trace`, `pkg/web`): list goFBP Networks and
  Services via `$SRV.STATS`, switch observing per Network, live Events via Server-Sent Events,
  embedded vanilla HTML/JS UI. goFBP reports the observing state as `ObserveStats` on the
  `observe` endpoint (2026-10-31).
- Smoke test with nats-server and two `textservice` instances: discovery, switching, SSE stream
  and stats work (2026-10-31).
- UI fixes after the first screenshot: Event table rendered via setTimeout instead of
  requestAnimationFrame (which does not run in background tabs), Time column shows
  HH:MM:SS.mmm instead of milliseconds only (2026-10-31).
- Logs tab: log records of all Services of a Network via `/api/networks/logs` (Log history of
  every Service, then live via the Log subject, joined by `seq`); level and text filter.
  `trace.Hub` now watches any subject (2026-10-31).
- Renamed from goFBPui to gofbpSRVLS: folder `gofbpSRVLS`, module `gofbpsrvls`, command
  `cmd/gofbpsrvls` (2026-10-31).
- gofbpstore (`cmd/gofbpstore`): records log records (always, incl. Log history) and Events
  (while trace is on) in SQLite, retention with hourly pruning, login (bcrypt, 12 h sessions),
  roles admin/viewer, user administration; Networks `record`, `prune`, `login`, `useradmin` with
  diagrams; pages moved to `pkg/webui` and shared with gofbpsrvls (2026-10-31).
- Fixes found while testing gofbpstore: `[hidden]` was overridden by `display: flex/grid`;
  `discover.List` returned "no responders" instead of an empty list when no Service runs (also
  affected gofbpsrvls) (2026-10-31).
- Review fixes: the last admin is now protected atomically in `pkg/store` (concurrent demote/delete
  of two admins left none before); the Collector asks again for a Log history that timed out
  instead of losing it; `events`/`logs` IDs use `AUTOINCREMENT` (with migration of older files),
  so open gofbpstore streams do not stall after pruning has emptied a table (2026-10-31).
- `README.md`: overview of both programs, setup, config, layout, tests and links to `docs/`
  (2026-11-06).
- Tests of the Networks `record`, `prune`, `login` and `useradmin` use `gofbp/pkg/fbptest`: they
  wait for the end of each Run instead of polling and check every Packet that leaves (e.g. all three
  `PruneResult`s); `RouteAction` and `CheckNewUser` are tested as single Bricks with `CallBrick`.
  goFBP updated to f10847a (2026-11-07).
- gofbpstore: password attempts limited to 5 per user name within 15 min (login and password change,
  429 with `Retry-After`, in memory); every user changes its own password via the new Network
  `password` (`POST /api/password`, Password page in the UI); it ends the user's other sessions
  (2026-11-07).
- gofbpsrvls: `trace.Hub` keeps a subject subscribed for 10 min after the last browser left, so
  the trace history survives a reload or switching Networks; Log histories are asked in parallel
  (1 s in all instead of 1 s per Service) (2026-11-07).
- Rest of the review (2026-10-31): gofbpstore shutdown waits for the HTTP handlers, Collector and
  pruning before closing the store; `-adduser` reads the password without echo from a terminal;
  `ReadHeaderTimeout` of 10 s in both programs; "Load older" stays hidden once nothing older is
  left and drops a page that arrives after the Network was switched; the Collector forgets
  Networks and Services not discovered for 5 min; `store.Open` escapes `%`, `?` and `#` in the
  path (2026-11-07).

## Open
- gofbpstore: no TLS of its own (reverse proxy). Streams poll SQLite every second per browser; fine
  for a few users. The login limit counts per user name only (no per-client limit, since behind a
  proxy all clients share an address), so trying one password against many names is not slowed
  beyond bcrypt, and a guesser can lock a user out for 15 min.
- Password page only checked via API tests and `node --check`, not in a browser.
- Viewer role in the UI only checked via API tests, not in a browser.
- UI checked in headless Firefox screenshots only (light mode, Events and Logs tab); check dark
  mode, the narrow layout and many Events/records in a real browser.
- automat-ai services do not call `slog.SetDefault(s.Logger())` yet, so only the records of the
  Service itself (not of `main` and the Bricks) reach the Logs tab.
- UI fixes of "Load older" only checked with `node --check`, not in a browser.
