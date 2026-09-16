const loginView = document.getElementById("login-view");
const dashboardView = document.getElementById("dashboard-view");
const loginForm = document.getElementById("login-form");
const loginError = document.getElementById("login-error");
const logoutBtn = document.getElementById("logout-btn");

const addModal = document.getElementById("add-modal");
const addForm = document.getElementById("add-form");
const addError = document.getElementById("add-error");
const addClientBtn = document.getElementById("add-client-btn");
const addCancelBtn = document.getElementById("add-cancel-btn");

const transportSelect = document.getElementById("f-transport");
const urlGroup = document.getElementById("f-url-group");
const maxGroup = document.getElementById("f-max-group");

const clientsList = document.getElementById("clients-list");
const clientsEmpty = document.getElementById("clients-empty");

let pollTimer = null;

async function api(path, opts) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  let body = null;
  try { body = await res.json(); } catch (_) {}
  if (!res.ok) {
    const msg = (body && body.error) || `HTTP ${res.status}`;
    throw new Error(msg);
  }
  return body;
}

function showDashboard() {
  loginView.style.display = "none";
  dashboardView.style.display = "block";
  refreshClients();
  if (!pollTimer) pollTimer = setInterval(refreshClients, 3000);
}

function showLogin() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  dashboardView.style.display = "none";
  loginView.style.display = "flex";
}

async function checkSession() {
  try {
    const s = await api("/api/session");
    if (s.authenticated) showDashboard();
    else showLogin();
  } catch (_) {
    showLogin();
  }
}

loginForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  loginError.textContent = "";
  const username = document.getElementById("login-user").value;
  const password = document.getElementById("login-pass").value;
  try {
    await api("/api/login", { method: "POST", body: JSON.stringify({ username, password }) });
    showDashboard();
  } catch (err) {
    loginError.textContent = "Неверный логин или пароль";
  }
});

logoutBtn.addEventListener("click", async () => {
  await api("/api/logout", { method: "POST" });
  showLogin();
});

function fieldsForTransport(t) {
  if (t === "oneme") {
    urlGroup.style.display = "none";
    maxGroup.style.display = "flex";
  } else {
    urlGroup.style.display = "flex";
    maxGroup.style.display = "none";
  }
}
transportSelect.addEventListener("change", () => fieldsForTransport(transportSelect.value));
fieldsForTransport(transportSelect.value);

addClientBtn.addEventListener("click", () => {
  addForm.reset();
  addError.textContent = "";
  fieldsForTransport(transportSelect.value);
  addModal.style.display = "flex";
});
addCancelBtn.addEventListener("click", () => { addModal.style.display = "none"; });
addModal.addEventListener("click", (e) => { if (e.target === addModal) addModal.style.display = "none"; });

addForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  addError.textContent = "";

  const cfg = {
    name: document.getElementById("f-name").value,
    transport: transportSelect.value,
    url: document.getElementById("f-url").value,
    max_token: document.getElementById("f-max-token").value,
    max_uid: document.getElementById("f-max-uid").value,
    codec: document.getElementById("f-codec").value,
    encryption_key_file: document.getElementById("f-enc-key").value,
  };

  try {
    await api("/api/clients", { method: "POST", body: JSON.stringify(cfg) });
    addModal.style.display = "none";
  } catch (err) {
    addError.textContent = err.message;
  } finally {
    // Refresh regardless: a failed-to-start client is still registered
    // (visible with an "error" status), so the operator should see it.
    refreshClients();
  }
});

function transportLabel(t) {
  return {
    yandex: "Yandex.Docs",
    vyandex: "Yandex.Docs (Volga)",
    oneme: "MAX Messenger",
    cupsonline: "Cups.online",
    mailru: "Mail.ru Docs",
  }[t] || t;
}

function formatUptime(seconds) {
  if (!seconds || seconds < 0) return "0с";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) return `${h}ч ${m}м`;
  if (m > 0) return `${m}м ${s}с`;
  return `${s}с`;
}

function renderClient(c) {
  const cfg = c.config;
  const div = document.createElement("div");
  div.className = "client-card";

  const dot = document.createElement("div");
  dot.className = "status-dot " + (c.status === "running" ? "running" : "error");
  div.appendChild(dot);

  const info = document.createElement("div");
  info.className = "client-info";

  const name = document.createElement("div");
  name.className = "client-name";
  name.textContent = cfg.name || cfg.id;
  info.appendChild(name);

  const meta = document.createElement("div");
  meta.className = "client-meta";
  meta.textContent = `${transportLabel(cfg.transport)} · id: ${cfg.id}`;
  info.appendChild(meta);

  if (c.status === "running" && c.stats) {
    const stats = document.createElement("div");
    stats.className = "client-stats";
    stats.textContent = `аптайм ${formatUptime(c.stats.UptimeSeconds)} · активных: ${c.stats.Established} · ретрансм.: ${c.stats.Retransmits}`;
    info.appendChild(stats);
  }

  if (c.error) {
    const err = document.createElement("div");
    err.className = "client-error";
    err.textContent = c.error;
    info.appendChild(err);
  }

  div.appendChild(info);

  const removeBtn = document.createElement("button");
  removeBtn.className = "remove-btn";
  removeBtn.textContent = "Удалить";
  removeBtn.addEventListener("click", async () => {
    if (!confirm(`Удалить клиента «${cfg.name || cfg.id}»?`)) return;
    try {
      await api(`/api/clients/${encodeURIComponent(cfg.id)}`, { method: "DELETE" });
      refreshClients();
    } catch (err) {
      alert("Не удалось удалить: " + err.message);
    }
  });
  div.appendChild(removeBtn);

  return div;
}

async function refreshClients() {
  let list;
  try {
    list = await api("/api/clients");
  } catch (err) {
    if (err.message.includes("401") || err.message.toLowerCase().includes("not authenticated")) {
      showLogin();
    }
    return;
  }

  clientsList.innerHTML = "";
  if (!list || list.length === 0) {
    clientsEmpty.style.display = "block";
    return;
  }
  clientsEmpty.style.display = "none";
  list
    .sort((a, b) => (a.config.name || a.config.id).localeCompare(b.config.name || b.config.id))
    .forEach((c) => clientsList.appendChild(renderClient(c)));
}

checkSession();
