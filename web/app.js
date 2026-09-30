// Demo UI for the Temporal Worker Controller.
//
// State arrives over server-sent events roughly once a second; the page is a
// pure function of that state plus a little local selection. Writes go to the
// API and are reflected on the next poll rather than being applied optimistically
// — the point of the demo is to watch the controller react.

const $ = (id) => document.getElementById(id);

let state = null;
let selectedID = null;
let detail = null;

// State arrives every second, but the diagram usually has not changed. Rebuilding
// the dots on every poll would make them flicker and would yank the element out
// from under a click, so each section re-renders only when its own inputs differ.
let lastVersionsKey = null;
let lastEventsKey = null;

/* ---------------- live state ---------------- */

function connect() {
  const es = new EventSource("/api/stream");

  es.onopen = () => setConn(true);
  es.onerror = () => setConn(false);
  es.onmessage = (e) => {
    state = JSON.parse(e.data);
    render();
    if (selectedID) refreshDetail();
  };
}

function setConn(ok) {
  const el = $("conn");
  el.textContent = ok ? "live" : "reconnecting";
  el.className = "pill " + (ok ? "pill-on" : "pill-off");
}

async function post(path, body) {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

/* ---------------- rendering ---------------- */

function render() {
  if (!state) return;

  $("deployment-name").textContent = state.deploymentName || "—";
  $("temporal-ui-link").href = state.temporalUi || "#";

  renderErrors();
  renderCRSummary();
  renderRollbackWarning();
  renderRouting();
  renderUnplaced();
  syncRolloutForm();

  const versionsKey = JSON.stringify(state.versions) + "|" + selectedID;
  if (versionsKey !== lastVersionsKey) {
    lastVersionsKey = versionsKey;
    renderVersions();
  }

  const events = state.events || [];
  const eventsKey = events.length + "|" + (events.length ? events[events.length - 1].at : "");
  if (eventsKey !== lastEventsKey) {
    lastEventsKey = eventsKey;
    renderEvents();
  }
}

// renderRollbackWarning explains, before you click Apply, that the controller
// will treat this rollout as a rollback and drop the ramp steps. Without it the
// ramp simply never happens and nothing says why.
function renderRollbackWarning() {
  const el = $("rollback-warning");
  const selected = $("rollout-image").value || "";
  const tag = selected.split(":").pop();

  const hit = (state.rollbackWindow || []).find((v) => v.tag === tag);
  const progressive = $("rollout-strategy").value === "Progressive";

  if (!hit || !progressive) {
    el.hidden = true;
    return;
  }

  const mins = Math.max(1, Math.round(hit.secondsAgo / 60));
  el.hidden = false;
  el.textContent =
    `${hit.buildId} was current ${mins} min ago. The controller treats a rollout to a version ` +
    `that was current in the last hour as a rollback: it goes to 100% at once and these ramp ` +
    `steps are ignored. Use an image that has not been current recently to show a real ramp.`;
}

function renderErrors() {
  const box = $("errors");
  const errs = state.errors || [];
  box.hidden = errs.length === 0;
  box.textContent = errs.join(" · ");
}

function renderCRSummary() {
  const cr = state.cr;
  const el = $("cr-summary");
  if (!cr || !cr.found) {
    el.textContent = "WorkerDeployment not found — run `make deploy`";
    return;
  }
  const parts = [cr.spec.image, cr.spec.strategy, `${cr.spec.replicas} replicas`];
  if (cr.status.targetBuildId && cr.status.targetBuildId !== cr.status.currentBuildId) {
    parts.push(`target ${cr.status.targetBuildId}`);
  }
  const notReady = (cr.status.conditions || []).find(
    (c) => c.type === "Ready" && c.status !== "True",
  );
  if (notReady && notReady.reason) parts.push(notReady.reason);
  el.textContent = parts.join("  ·  ");
}

// renderRouting shows the deployment's ramp while one is in progress. Outside a
// Progressive rollout there is no ramp, and the row stays out of the way.
function renderRouting() {
  const row = $("routing");
  if (!state.rampingBuildId) {
    row.hidden = true;
    return;
  }

  const pct = state.rampPercentage || 0;
  row.hidden = false;
  $("routing-version").textContent = state.rampingBuildId;
  $("routing-fill").style.width = `${pct}%`;
  $("routing-pct").textContent = `${pct.toFixed(pct % 1 ? 1 : 0)}%`;
}

function renderVersions() {
  const wrap = $("versions");
  wrap.textContent = "";

  const versions = state.versions || [];
  if (versions.length === 0) {
    const p = document.createElement("div");
    p.className = "placeholder";
    p.textContent =
      "No worker versions yet. Once the controller starts worker pods and they poll Temporal, a version box appears here.";
    wrap.append(p);
    return;
  }

  versions.forEach((v, i) => wrap.append(versionBox(v, i)));
}

function versionBox(v, index) {
  const box = document.createElement("div");
  box.className = `version accent-${index % 4}`;
  if (v.status === "Draining" || v.status === "Drained") box.classList.add("is-draining");

  const title = document.createElement("div");
  title.className = "version-title";
  title.textContent = `Version ${v.buildId}`;
  box.append(title);

  const sub = document.createElement("div");
  sub.className = "version-sub";
  sub.append(span(`${v.ready}/${v.desired} pods`));
  sub.append(span(`${v.workflows ? v.workflows.length : 0} running`));
  if (v.pinned) sub.append(span(`${v.pinned} pinned`));
  if (v.failing) {
    const f = span(`${v.failing} failing`);
    f.style.color = "var(--danger)";
    sub.append(f);
  }
  box.append(sub);

  const dots = document.createElement("div");
  dots.className = "dots";
  (v.workflows || []).forEach((wf) => dots.append(dotFor(wf)));
  box.append(dots);

  const foot = document.createElement("div");
  foot.className = "version-foot";
  foot.append(statusBadge(v));

  // Wedged orders take precedence over the routine move. The only thing that
  // actually frees them is putting the deployment back on the version they were
  // running: a per-execution override does not abandon an in-flight transition,
  // so the next workflow task is still handed to the version that cannot replay.
  const rollbackImage = imageForBuildID(v.buildId);
  if (v.failing > 0 && rollbackImage && v.buildId !== state.currentBuildId) {
    const btn = document.createElement("button");
    btn.className = "btn btn-primary btn-small";
    // The tag alone keeps the button from crowding the status badge; the full
    // build ID is on the tooltip.
    btn.textContent = `Roll back to ${rollbackImage.split(":").pop()}`;
    btn.title = `Put the deployment back on ${v.buildId}`;
    btn.onclick = () =>
      guard(btn, () =>
        post("/api/rollout", {
          image: rollbackImage,
          strategy: "AllAtOnce",
          steps: [],
        }),
      );
    foot.append(btn);
  } else if (v.pinned > 0 && v.buildId !== state.currentBuildId && state.currentBuildId) {
    const btn = document.createElement("button");
    btn.className = "btn btn-ghost btn-small";
    btn.textContent = `Upgrade ${v.pinned} pinned`;
    btn.onclick = () =>
      guard(btn, () => post("/api/workflows/upgrade-pinned", { fromBuildId: v.buildId }));
    foot.append(btn);
  }
  box.append(foot);

  return box;
}

function statusBadge(v) {
  const b = document.createElement("span");
  if (v.status === "Current") {
    b.className = "badge badge-current";
    b.innerHTML = "&#9825; Current";
  } else if (v.status === "Ramping") {
    b.className = "badge badge-ramping";
    b.textContent = `Ramping ${Math.round(v.rampPercentage)}%`;
  } else if (v.status === "Draining" || v.status === "Drained") {
    b.className = "badge badge-draining";
    b.innerHTML = `&#8659; ${v.status}`;
  } else if (v.isTarget) {
    b.className = "badge badge-target";
    b.textContent = "Target";
  } else {
    b.className = "badge";
    b.textContent = v.status;
  }
  return b;
}

function dotFor(wf) {
  const failing = (wf.problems || []).length > 0;

  const d = document.createElement("button");
  d.className = "dot";
  if (wf.behavior === "Pinned") d.classList.add("is-pinned");
  if (failing) d.classList.add("is-failing");
  if (wf.id === selectedID) d.classList.add("is-selected");

  d.title = [wf.id, wf.behavior || "unversioned", ...(wf.problems || [])].join("\n");
  d.onclick = () => {
    selectedID = wf.id;
    detail = null;
    renderVersions();
    renderDetail();
    refreshDetail();
  };
  return d;
}

function renderUnplaced() {
  const el = $("unplaced");
  const n = (state.unplaced || []).length;
  el.hidden = n === 0;
  if (n > 0) {
    el.textContent = `${n} order(s) just started and have not completed a workflow task yet, so Temporal has not placed them on a version.`;
  }
}

function renderEvents() {
  const ul = $("events");
  ul.textContent = "";
  const events = (state.events || []).slice(-80).reverse();
  for (const ev of events) {
    const li = document.createElement("li");
    const t = document.createElement("time");
    t.textContent = new Date(ev.at).toLocaleTimeString();
    const s = document.createElement("span");
    s.className = "kind-" + (ev.kind || "info");
    s.textContent = ev.text;
    li.append(t, s);
    ul.append(li);
  }
}

function span(text) {
  const s = document.createElement("span");
  s.textContent = text;
  return s;
}

/* ---------------- detail panel ---------------- */

async function refreshDetail() {
  if (!selectedID) return;
  try {
    const res = await fetch(`/api/workflows/${encodeURIComponent(selectedID)}`);
    detail = res.ok ? await res.json() : null;
  } catch {
    detail = null;
  }
  renderDetail();
}

// deselect clears the selection and repaints the diagram so the selected dot
// loses its outline.
function deselect() {
  selectedID = null;
  detail = null;
  renderVersions();
  renderDetail();
}

function renderDetail() {
  const body = $("detail-body");
  body.textContent = "";
  $("detail-close").hidden = !selectedID;

  if (!selectedID) {
    body.className = "detail-body empty";
    body.textContent = "Click a dot to inspect an order.";
    return;
  }
  body.className = "detail-body";

  if (!detail) {
    body.textContent = `Loading ${selectedID}…`;
    return;
  }

  if ((detail.problems || []).length) {
    const box = document.createElement("div");
    box.className = "problem";
    box.textContent = detail.problems.join("  ·  ");
    // Without this the dot looks broken on the version it was happily running
    // on: a failing execution stays filed under the last version that completed
    // a task for it, while the version it cannot replay against is the new one.
    if (detail.transitioningTo) {
      box.textContent += `  —  while moving to ${detail.transitioningTo}`;
    }
    body.append(box);
  }

  const dl = document.createElement("dl");
  addRow(dl, "Order", detail.id);
  addRow(dl, "Type", detail.type);
  // Show the behaviour that governs the execution from here on; an override
  // takes precedence over what the last workflow task reported.
  addRow(dl, "Behaviour", detail.effectiveBehavior || detail.behavior || "—");
  addRow(dl, "Version", detail.buildId || "—");
  if (detail.transitioningTo) addRow(dl, "Moving to", detail.transitioningTo);
  addRow(dl, "Override", detail.override || "none");
  body.append(dl);

  if (detail.state) {
    const st = detail.state;
    const label = document.createElement("div");
    label.className = "hint hint-tight";
    label.textContent = `Step ${st.step} of ${st.totalSteps}`;
    body.append(label);

    const bar = document.createElement("div");
    bar.className = "progress";
    const fill = document.createElement("div");
    fill.style.width = `${st.totalSteps ? (st.step / st.totalSteps) * 100 : 0}%`;
    bar.append(fill);
    body.append(bar);

    body.append(historyView(st.history || []));
  }

  body.append(detailActions());
}

// historyView highlights the step where an order changed worker version, which
// is the thing the diagram alone cannot show.
function historyView(history) {
  const box = document.createElement("div");
  box.className = "history";
  let previous = null;
  for (const line of history.slice(-40)) {
    const version = line.split(":")[0];
    const row = document.createElement("div");
    row.textContent = line;
    if (previous !== null && version !== previous) {
      row.className = "switch";
      row.textContent = `${line}   ← moved to ${version}`;
    }
    previous = version;
    box.append(row);
  }
  if (history.length === 0) box.textContent = "No steps completed yet.";
  return box;
}

function detailActions() {
  const wrap = document.createElement("div");
  wrap.className = "detail-actions";

  wrap.append(temporalUiLink());

  const current = state.currentBuildId;
  if (current && detail.buildId !== current && !(detail.problems || []).length) {
    wrap.append(
      actionButton("btn", `Pin to current (${current})`, () =>
        post("/api/workflows/versioning", {
          workflowId: detail.id,
          mode: "pinned",
          buildId: current,
        }),
      ),
    );
  }
  wrap.append(
    actionButton("btn", "Make auto-upgrade", () =>
      post("/api/workflows/versioning", { workflowId: detail.id, mode: "autoUpgrade" }),
    ),
  );
  if (detail.override) {
    wrap.append(
      actionButton("btn btn-ghost", "Clear override", () =>
        post("/api/workflows/versioning", { workflowId: detail.id, mode: "clear" }),
      ),
    );
  }
  return wrap;
}

// temporalUiLink opens this execution in the Temporal Web UI. Deep-linking to
// the run rather than the workflow means it still resolves after a reset or
// continue-as-new has produced newer runs.
// imageForBuildID recovers the image a version was built from. The controller
// derives a build ID as "<image tag>-<pod template hash>", so the tag is the
// part before the final dash — only trusted when it matches an image this demo
// actually offers.
function imageForBuildID(buildId) {
  const tag = (buildId || "").replace(/-[^-]*$/, "");
  if (!tag || !(state.imageTags || []).includes(tag)) return null;
  return `${state.imageRepo}:${tag}`;
}

function temporalUiLink() {
  const a = document.createElement("a");
  a.className = "btn btn-ghost";
  a.textContent = "Open in Temporal UI \u2197";
  a.target = "_blank";
  a.rel = "noreferrer";

  const base = (state.temporalUi || "").replace(/\/$/, "");
  const ns = encodeURIComponent(state.temporalNamespace || "default");
  const id = encodeURIComponent(detail.id);
  a.href = detail.runId
    ? `${base}/namespaces/${ns}/workflows/${id}/${encodeURIComponent(detail.runId)}/history`
    : `${base}/namespaces/${ns}/workflows/${id}`;

  return a;
}

function actionButton(cls, text, fn) {
  const b = document.createElement("button");
  b.className = cls;
  b.textContent = text;
  b.onclick = () => guard(b, async () => {
    await fn();
    await refreshDetail();
  });
  return b;
}

function addRow(dl, term, value) {
  const dt = document.createElement("dt");
  dt.textContent = term;
  const dd = document.createElement("dd");
  dd.textContent = value;
  dl.append(dt, dd);
}

/* ---------------- rollout form ---------------- */

let formInitialised = false;

// syncRolloutForm fills the form from the live resource the first time only.
// After that the fields are the presenter's to edit; overwriting them on every
// poll would fight whoever is typing.
function syncRolloutForm() {
  const select = $("rollout-image");
  if (select.options.length === 0 && state.imageTags) {
    for (const tag of state.imageTags) {
      const o = document.createElement("option");
      o.value = `${state.imageRepo}:${tag}`;
      o.textContent = tag === "v3" ? `${tag} (breaking change)` : tag;
      select.append(o);
    }
  }

  if (formInitialised || !state.cr || !state.cr.found) return;
  formInitialised = true;

  if (state.cr.spec.image) select.value = state.cr.spec.image;
  $("rollout-strategy").value = state.cr.spec.strategy || "AllAtOnce";
  if (state.cr.spec.steps && state.cr.spec.steps.length) setSteps(state.cr.spec.steps);
  toggleSteps();
}

const DEFAULT_STEPS = [
  { rampPercentage: 25, pauseDuration: "30s" },
  { rampPercentage: 50, pauseDuration: "30s" },
];

function setSteps(steps) {
  const wrap = $("steps");
  wrap.textContent = "";
  steps.forEach((s) => wrap.append(stepRow(s)));
}

function stepRow(s) {
  const row = document.createElement("div");
  row.className = "step-row";

  const pct = document.createElement("input");
  pct.type = "number";
  pct.min = 1;
  pct.max = 99;
  pct.value = s.rampPercentage;
  pct.dataset.role = "pct";

  const pause = document.createElement("input");
  pause.type = "text";
  pause.value = s.pauseDuration || "30s";
  pause.dataset.role = "pause";

  const del = document.createElement("button");
  del.type = "button";
  del.className = "btn btn-ghost btn-small";
  del.textContent = "×";
  del.onclick = () => row.remove();

  row.append(pct, pause, del);
  return row;
}

function readSteps() {
  return [...$("steps").querySelectorAll(".step-row")].map((row) => ({
    rampPercentage: Number(row.querySelector('[data-role="pct"]').value),
    pauseDuration: row.querySelector('[data-role="pause"]').value.trim(),
  }));
}

function toggleSteps() {
  $("steps-wrap").hidden = $("rollout-strategy").value !== "Progressive";
}

/* ---------------- actions ---------------- */

// guard disables a button while its request is in flight and surfaces failures
// where the presenter will see them.
async function guard(button, fn) {
  const label = button.textContent;
  button.disabled = true;
  try {
    await fn();
  } catch (err) {
    alert(err.message);
  } finally {
    button.disabled = false;
    button.textContent = label;
  }
}

function wireUp() {
  setSteps(DEFAULT_STEPS);
  toggleSteps();

  $("start-btn").onclick = (e) =>
    guard(e.target, () =>
      post("/api/workflows", {
        count: Number($("start-count").value),
        behavior: document.querySelector('input[name="behavior"]:checked').value,
      }),
    );

  $("rollout-strategy").onchange = () => {
    toggleSteps();
    renderRollbackWarning();
  };
  $("rollout-image").onchange = renderRollbackWarning;
  $("add-step").onclick = () => $("steps").append(stepRow({ rampPercentage: 50, pauseDuration: "30s" }));

  $("rollout-btn").onclick = (e) =>
    guard(e.target, () =>
      post("/api/rollout", {
        image: $("rollout-image").value,
        strategy: $("rollout-strategy").value,
        steps: $("rollout-strategy").value === "Progressive" ? readSteps() : [],
      }),
    );

  $("reset-btn").onclick = (e) =>
    guard(e.target, async () => {
      if (!confirm("Terminate every running order?")) return;
      await post("/api/reset");
      deselect();
    });

  $("detail-close").onclick = deselect;

  // Escape is the expected way out of a detail view, and it keeps the presenter
  // off the mouse.
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && selectedID) deselect();
  });
}

wireUp();
connect();
