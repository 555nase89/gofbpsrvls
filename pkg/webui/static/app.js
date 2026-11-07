// Shared by gofbpsrvls and gofbpstore: lists goFBP Networks, switches
// observing and shows traced Events and the log records of their Services.
// GET api/me tells the two apart: gofbpsrvls has no such route (404), while
// gofbpstore needs a login and reads Events and log records from its store.
"use strict";

const MAX_EVENTS = 2000;
const MAX_LOGS = 2000;
const REFRESH_MS = 5000;

const $ = (id) => document.getElementById(id);
let networks = [];
let selected = null; // base subject
let source = null; // EventSource of the selected Network
let events = []; // EventData of the selected Network, oldest first
let paused = false;
let tab = "events";
let logSource = null; // EventSource of the logs of the selected Network
let logs = []; // LogData of the selected Network, oldest first
let logsPaused = false;
let me = null; // logged-in user of gofbpstore; null in gofbpsrvls
let noOlder = { events: false, logs: false }; // "Load older" found nothing more
let refreshTimer = null;

function el(tag, text, cls) {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

function observing(n) {
  const on = n.services.filter((s) => s.observing).length;
  return { any: on > 0, all: on === n.services.length };
}

// running is false for a Network gofbpstore only knows from its store.
const running = (n) => n.running !== false;

// show displays one of the pages "main", "login", "password" or "admin".
function show(page) {
  $("main").hidden = page !== "main";
  $("login").hidden = page !== "login";
  $("password").hidden = page !== "password";
  $("admin").hidden = page !== "admin";
  for (const id of ["refresh", "status"]) $(id).hidden = page !== "main";
  $("me").hidden = $("logout").hidden = !me;
  $("users-btn").hidden = !me || me.role !== "admin" || page === "admin";
  $("password-btn").hidden = !me || page === "password";
  if (page !== "main") { stopStream(); stopLogs(); }
}

function loggedOut() {
  me = null;
  clearInterval(refreshTimer);
  refreshTimer = null;
  show("login");
  $("login-name").focus();
}

function startRefresh() {
  show("main");
  refresh();
  if (!refreshTimer) refreshTimer = setInterval(refresh, REFRESH_MS);
}

async function boot() {
  const r = await fetch("api/me");
  if (r.status === 401) return loggedOut();
  if (r.ok) {
    me = await r.json();
    $("me").textContent = me.name + " (" + me.role + ")";
    if (location.hash === "#users" && me.role === "admin") return showUsers();
    if (location.hash === "#password") return showPassword();
  }
  startRefresh();
}

async function refresh() {
  try {
    const r = await fetch("api/networks");
    if (r.status === 401) return loggedOut();
    if (!r.ok) throw new Error(await r.text());
    networks = await r.json();
    $("status").textContent = "updated " + new Date().toLocaleTimeString();
  } catch (e) {
    $("status").textContent = "error: " + e.message;
    return;
  }
  if (selected && !networks.some((n) => n.base === selected)) selected = null;
  if (!selected && networks.length) selected = networks[0].base;
  render();
}

function render() {
  const list = $("networks");
  list.replaceChildren();
  $("empty").hidden = networks.length > 0;
  for (const n of networks) {
    const li = el("li");
    if (n.base === selected) li.classList.add("selected");
    if (!running(n)) li.classList.add("stopped");
    const name = el("div", undefined, "name");
    name.append(el("span", n.name));
    const obs = observing(n);
    const badge = !running(n) ? "stopped" : obs.any ? "trace" : n.services.length + "×";
    name.append(el("span", badge, obs.any ? "badge on" : "badge"));
    li.append(name, el("div", n.package || n.base, "pkg muted mono"));
    li.onclick = () => { selected = n.base; render(); };
    list.append(li);
  }
  renderDetail(networks.find((n) => n.base === selected));
}

function renderDetail(n) {
  $("detail").hidden = !n;
  if (!n) { stopStream(); stopLogs(); return; }
  $("d-name").textContent = n.name;
  $("d-base").textContent = n.base;
  $("d-inports").textContent = n.inports.join(", ");
  $("d-outports").textContent = n.outports.join(", ");
  const obs = observing(n);
  $("d-trace").checked = obs.any;
  $("d-trace").disabled = !running(n) || (me && me.role !== "admin");
  $("d-trace").parentElement.title = $("d-trace").disabled
    ? (running(n) ? "Only admins switch trace" : "Network not running")
    : "Switches observing for all Services of this Network";
  $("d-mixed").hidden = !obs.any || obs.all;
  $("d-stopped").hidden = running(n);
  $("d-services-table").hidden = !running(n);
  document.querySelector(".ports").hidden = !running(n);

  const body = $("d-services");
  body.replaceChildren();
  for (const s of n.services) {
    const tr = el("tr");
    tr.append(
      el("td", s.id.slice(0, 8), "mono"),
      el("td", s.version, "mono"),
      el("td", new Date(s.started).toLocaleString()),
    );
    const t = el("td");
    t.append(el("span", s.observing ? "on" : "off", s.observing ? "badge on" : "badge"));
    tr.append(
      t,
      el("td", s.received, "num"),
      el("td", s.errors, "num"),
      el("td", s.dropped, "num"),
      el("td", Object.entries(s.outports).map(([k, v]) => k + " " + v).join(", "), "mono"),
    );
    body.append(tr);
  }

  // gofbpstore shows recorded Events also while trace is off.
  const showEvents = obs.any || me !== null;
  $("trace").hidden = !showEvents;
  $("trace-off").hidden = obs.any || !running(n);
  $("trace-off").textContent = me
    ? "Trace is off: no new Events are recorded."
    : "Trace is off. Switch it on to see the Events of this Network.";
  if (showEvents) startStream(n.base); else stopStream();
  if (tab === "logs") startLogs(n.base); else stopLogs();
  showOlder();
}

// showOlder shows the "Load older" buttons in gofbpstore until they found
// no older rows for the current stream.
function showOlder() {
  $("older").hidden = !me || noOlder.events;
  $("log-older").hidden = !me || noOlder.logs;
}

function startStream(base) {
  if (source && source.base === base) return;
  stopStream();
  events = [];
  noOlder.events = false;
  renderEvents();
  source = new EventSource("api/networks/events?base=" + encodeURIComponent(base));
  source.base = base;
  // After a reconnect the server sends its recent Events again.
  source.onopen = () => { events = []; noOlder.events = false; showOlder(); scheduleRender(); };
  source.onmessage = (m) => {
    events.push(JSON.parse(m.data));
    if (events.length > MAX_EVENTS) events.splice(0, events.length - MAX_EVENTS);
    if (!paused) scheduleRender();
  };
}

function stopStream() {
  if (source) source.close();
  source = null;
}

// later returns a function that calls fn at most every 100 ms. Not
// requestAnimationFrame: it does not run in background tabs, which would
// leave the table empty until the tab is shown.
function later(fn) {
  let pending = false;
  return () => {
    if (pending) return;
    pending = true;
    setTimeout(() => { pending = false; fn(); }, 100);
  };
}
const scheduleRender = later(() => renderEvents());
const scheduleLogs = later(() => renderLogs());

function startLogs(base) {
  if (logSource && logSource.base === base) return;
  stopLogs();
  logs = [];
  noOlder.logs = false;
  renderLogs();
  // The server sends the Log history of every Service first, then live records.
  logSource = new EventSource("api/networks/logs?base=" + encodeURIComponent(base));
  logSource.base = base;
  // After a reconnect the server sends the history again.
  logSource.onopen = () => { logs = []; noOlder.logs = false; showOlder(); scheduleLogs(); };
  logSource.onmessage = (m) => {
    logs.push(JSON.parse(m.data));
    if (logs.length > MAX_LOGS) logs.splice(0, logs.length - MAX_LOGS);
    if (!logsPaused) scheduleLogs();
  };
}

function stopLogs() {
  if (logSource) logSource.close();
  logSource = null;
}

const LEVELS = { DEBUG: -4, INFO: 0, WARN: 4, ERROR: 8 };

// level splits a slog level name like "INFO" or "WARN+2" into its base name
// and numeric value.
function level(s) {
  const m = /^([A-Z]+)([+-]\d+)?$/.exec(s) || [];
  const name = m[1] in LEVELS ? m[1] : "INFO";
  return { name, value: LEVELS[name] + Number(m[2] || 0) };
}

function attrsText(attrs) {
  if (!attrs) return "";
  return Object.entries(attrs)
    .map(([k, v]) => k + "=" + (typeof v === "string" ? v : JSON.stringify(v)))
    .join(" ");
}

function renderLogs() {
  const min = Number($("log-level").value);
  const f = $("log-filter").value.trim().toLowerCase();
  const shown = logs.filter((d) => level(d.level).value >= min &&
    (!f || [d.service, d.message, attrsText(d.attrs)].some((v) => v.toLowerCase().includes(f))));
  const body = $("logs");
  body.replaceChildren();
  // Newest first.
  for (let i = shown.length - 1; i >= 0; i--) {
    const d = shown[i];
    const tr = el("tr", undefined, level(d.level).name);
    const svc = el("td", d.service.slice(0, 8), "svc");
    svc.title = "Filter by Service " + d.service;
    svc.onclick = () => { $("log-filter").value = d.service; renderLogs(); };
    tr.append(
      el("td", new Date(d.time).toLocaleTimeString(undefined, { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3 })),
      svc,
      el("td", d.level, "level"),
      el("td", d.message, "msg"),
      el("td", attrsText(d.attrs), "data"),
    );
    body.append(tr);
  }
  $("log-count").textContent = shown.length === logs.length
    ? logs.length + " records"
    : shown.length + " of " + logs.length + " records";
}

function showTab(name) {
  tab = name;
  $("tab-events").classList.toggle("active", name === "events");
  $("tab-logs").classList.toggle("active", name === "logs");
  $("pane-events").hidden = name !== "events";
  $("pane-logs").hidden = name !== "logs";
  history.replaceState(null, "", name === "logs" ? "#logs" : location.pathname);
  render();
}

function matches(e, f) {
  if (!f) return true;
  return [e.run_id, e.node, e.port, e.kind, e.service, e.error, JSON.stringify(e.data)]
    .some((v) => v && String(v).toLowerCase().includes(f));
}

function renderEvents() {
  const f = $("filter").value.trim().toLowerCase();
  const shown = events.filter((e) => matches(e, f));
  const body = $("events");
  body.replaceChildren();
  // Newest first.
  for (let i = shown.length - 1; i >= 0; i--) {
    const e = shown[i];
    const tr = el("tr");
    if (e.error) tr.classList.add("error");
    const run = el("td", e.run_id.slice(0, 8), "run");
    run.title = "Filter by Run ID " + e.run_id;
    run.onclick = () => { $("filter").value = e.run_id; renderEvents(); };
    const packet = e.packet_id ? e.packet_id + (e.parent_id ? " ← " + e.parent_id : "") : "";
    const copy = e.copy ? " #" + e.copy : "";
    tr.append(
      el("td", new Date(e.time).toLocaleTimeString(undefined, { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3 })),
      el("td", e.service.slice(0, 8)),
      el("td", e.kind),
      run,
      el("td", packet + copy),
      el("td", e.node || ""),
      el("td", e.port || ""),
      el("td", e.error || (e.data === undefined ? "" : JSON.stringify(e.data)), "data"),
    );
    body.append(tr);
  }
  $("count").textContent = shown.length === events.length
    ? events.length + " events"
    : shown.length + " of " + events.length + " events";
}

$("d-trace").onchange = async (ev) => {
  const on = ev.target.checked;
  try {
    const r = await fetch("api/networks/observe", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ base: selected, on }),
    });
    if (!r.ok) throw new Error(await r.text());
  } catch (e) {
    $("status").textContent = "error: " + e.message;
  }
  refresh();
};
// loadOlder fetches the page before the oldest shown row of kind ("events"
// or "logs") from gofbpstore and returns it, or null if there is nothing to
// add: also when another Network was selected or the list was restarted or
// trimmed while waiting, as the page would no longer fit in front of it.
async function loadOlder(kind) {
  const list = () => (kind === "events" ? events : logs);
  const base = selected;
  const first = list()[0];
  if (!first) return null;
  const r = await fetch("api/networks/" + kind + "?base=" + encodeURIComponent(base) + "&before=" + first.id);
  if (r.status === 401) { loggedOut(); return null; }
  if (!r.ok) { $("status").textContent = "error: " + await r.text(); return null; }
  const older = await r.json();
  if (selected !== base || list()[0] !== first) return null;
  if (!older.length) { noOlder[kind] = true; showOlder(); return null; }
  return older;
}
$("older").onclick = async () => {
  const older = await loadOlder("events");
  if (older) { events = older.concat(events); renderEvents(); }
};
$("log-older").onclick = async () => {
  const older = await loadOlder("logs");
  if (older) { logs = older.concat(logs); renderLogs(); }
};

$("login-form").onsubmit = async (ev) => {
  ev.preventDefault();
  const r = await fetch("api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name: $("login-name").value, password: $("login-password").value }),
  });
  $("login-password").value = "";
  if (!r.ok) {
    $("login-error").textContent = (await r.text()).trim();
    $("login-error").hidden = false;
    return;
  }
  $("login-error").hidden = true;
  boot();
};
$("logout").onclick = async () => {
  await fetch("api/logout", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
  loggedOut();
};

async function postUser(change) {
  const r = await fetch("api/users", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(change),
  });
  if (r.status === 401) return loggedOut();
  const err = $("admin-error");
  err.hidden = r.ok;
  if (!r.ok) err.textContent = (await r.text()).trim();
  return r.ok;
}

async function renderUsers() {
  const r = await fetch("api/users");
  if (r.status === 401) return loggedOut();
  if (!r.ok) { $("admin-error").textContent = await r.text(); $("admin-error").hidden = false; return; }
  const body = $("users");
  body.replaceChildren();
  for (const u of await r.json()) {
    const tr = el("tr");
    const role = el("select");
    for (const v of ["viewer", "admin"]) {
      const o = el("option", v);
      o.value = v;
      o.selected = u.role === v;
      role.append(o);
    }
    const pw = el("input");
    pw.type = "password";
    pw.placeholder = "unchanged";
    pw.autocomplete = "new-password";
    const save = el("button", "Save");
    save.type = "button";
    save.onclick = async () => {
      if (await postUser({ action: "update", name: u.name, role: role.value, password: pw.value })) renderUsers();
    };
    const del = el("button", "Delete");
    del.type = "button";
    del.onclick = async () => {
      if (confirm("Delete user " + u.name + "?") && await postUser({ action: "delete", name: u.name })) renderUsers();
    };
    const tdRole = el("td"); tdRole.append(role);
    const tdPw = el("td"); tdPw.append(pw);
    const tdAct = el("td"); tdAct.append(save, " ", del);
    tr.append(el("td", u.name), tdRole, el("td", new Date(u.created).toLocaleString()), tdPw, tdAct);
    body.append(tr);
  }
}

function showUsers() {
  history.replaceState(null, "", "#users");
  show("admin");
  renderUsers();
}
$("users-btn").onclick = showUsers;
$("admin-close").onclick = () => { showTab(tab); startRefresh(); };
$("user-form").onsubmit = async (ev) => {
  ev.preventDefault();
  const ok = await postUser({ action: "create", name: $("new-name").value, password: $("new-password").value, role: $("new-role").value });
  if (ok) {
    $("new-name").value = $("new-password").value = "";
    renderUsers();
  }
};

function showPassword() {
  history.replaceState(null, "", "#password");
  for (const id of ["pw-current", "pw-new", "pw-repeat"]) $(id).value = "";
  $("pw-error").hidden = $("pw-done").hidden = true;
  show("password");
  $("pw-current").focus();
}
$("password-btn").onclick = showPassword;
$("password-close").onclick = () => { showTab(tab); startRefresh(); };
$("password-form").onsubmit = async (ev) => {
  ev.preventDefault();
  const err = $("pw-error");
  $("pw-done").hidden = true;
  if ($("pw-new").value !== $("pw-repeat").value) {
    err.textContent = "The new passwords do not match.";
    err.hidden = false;
    return;
  }
  const r = await fetch("api/password", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password: $("pw-current").value, new_password: $("pw-new").value }),
  });
  if (r.status === 401) return loggedOut();
  for (const id of ["pw-current", "pw-new", "pw-repeat"]) $(id).value = "";
  err.hidden = r.ok;
  $("pw-done").hidden = !r.ok;
  if (!r.ok) err.textContent = (await r.text()).trim();
};

$("refresh").onclick = refresh;
$("filter").oninput = renderEvents;
$("pause").onclick = () => {
  paused = !paused;
  $("pause").textContent = paused ? "Resume" : "Pause";
  if (!paused) renderEvents();
};
$("clear").onclick = () => { events = []; renderEvents(); };
$("tab-events").onclick = () => showTab("events");
$("tab-logs").onclick = () => showTab("logs");
$("log-level").onchange = renderLogs;
$("log-filter").oninput = renderLogs;
$("log-pause").onclick = () => {
  logsPaused = !logsPaused;
  $("log-pause").textContent = logsPaused ? "Resume" : "Pause";
  if (!logsPaused) renderLogs();
};
$("log-clear").onclick = () => { logs = []; renderLogs(); };

if (location.hash === "#logs") showTab("logs");
boot();
