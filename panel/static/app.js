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
const cupsonlineNote = document.getElementById("f-cupsonline-note");
const urlHint = document.getElementById("f-url-hint");
const urlInput = document.getElementById("f-url");
const urlGenerateBtn = document.getElementById("f-url-generate");
const urlGenerateError = document.getElementById("f-url-generate-error");
const nameInput = document.getElementById("f-name");

const MULTISTREAM_TRANSPORTS = new Set(["yandex", "vyandex"]);
const modalTitle = document.getElementById("add-modal-title");
const submitBtn = document.getElementById("add-submit-btn");

const clientsList = document.getElementById("clients-list");
const clientsEmpty = document.getElementById("clients-empty");

const qrModal = document.getElementById("qr-modal");
const qrImage = document.getElementById("qr-image");
const qrError = document.getElementById("qr-error");
const qrCloseBtn = document.getElementById("qr-close-btn");

const panelKeyValue = document.getElementById("panel-key-value");
const panelKeyCopyBtn = document.getElementById("panel-key-copy");

let pollTimer = null;
// Client id being edited, or null when the modal is in "add" mode.
let editingID = null;
let yandexDocAvailable = false;

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
  loadPanelKey();
  loadYandexDocAvailability();
  refreshClients();
  if (!pollTimer) pollTimer = setInterval(refreshClients, 3000);
}

async function loadYandexDocAvailability() {
  try {
    const { available } = await api("/api/yandex-doc-available");
    yandexDocAvailable = !!available;
  } catch (_) {
    yandexDocAvailable = false;
  }
  fieldsForTransport(transportSelect.value);
}

async function loadPanelKey() {
  try {
    const { public_key } = await api("/api/panel-key");
    panelKeyValue.textContent = public_key || "(не задан)";
  } catch (_) {
    panelKeyValue.textContent = "не удалось загрузить";
  }
}

panelKeyCopyBtn.addEventListener("click", async () => {
  const key = panelKeyValue.textContent;
  try {
    await navigator.clipboard.writeText(key);
    const original = panelKeyCopyBtn.textContent;
    panelKeyCopyBtn.textContent = "Скопировано";
    setTimeout(() => { panelKeyCopyBtn.textContent = original; }, 1500);
  } catch (_) {
    // Clipboard API unavailable (e.g. non-secure context) -- select the
    // text so the operator can still copy it manually.
    const range = document.createRange();
    range.selectNodeContents(panelKeyValue);
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
  }
});

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
  cupsonlineNote.style.display = "none";
  urlHint.style.display = "none";
  urlGenerateError.textContent = "";
  if (t === "oneme") {
    urlGroup.style.display = "none";
    maxGroup.style.display = "flex";
  } else if (t === "cupsonline") {
    // The exit generates its own rooms on start; cfg.url is ignored server
    // side for cupsonline exit clients (unlike every other transport), so
    // asking for one here would just be misleading.
    urlGroup.style.display = "none";
    maxGroup.style.display = "none";
    cupsonlineNote.style.display = "block";
  } else {
    urlGroup.style.display = "flex";
    maxGroup.style.display = "none";
    if (MULTISTREAM_TRANSPORTS.has(t)) urlHint.style.display = "block";
  }
  // Only yandex/vyandex documents can be auto-generated (that's what
  // yandexdisk.CreateDoc produces); oneme/cupsonline/mailru don't apply.
  const canGenerate = yandexDocAvailable && (t === "yandex" || t === "vyandex");
  urlGenerateBtn.style.display = canGenerate ? "inline-block" : "none";
}
transportSelect.addEventListener("change", () => fieldsForTransport(transportSelect.value));
fieldsForTransport(transportSelect.value);

function openAddModal() {
  editingID = null;
  addForm.reset();
  addError.textContent = "";
  modalTitle.textContent = "Новый клиент";
  submitBtn.textContent = "Добавить";
  fieldsForTransport(transportSelect.value);
  addModal.style.display = "flex";
}

function openEditModal(status) {
  const cfg = status.config;
  editingID = cfg.id;
  addError.textContent = "";
  modalTitle.textContent = "Редактировать клиента";
  submitBtn.textContent = "Сохранить";

  document.getElementById("f-name").value = cfg.name || "";
  transportSelect.value = cfg.transport;
  document.getElementById("f-url").value = cfg.url || "";
  document.getElementById("f-max-token").value = cfg.max_token || "";
  document.getElementById("f-max-uid").value = cfg.max_uid || "";
  document.getElementById("f-codec").value = cfg.codec || "";
  // The PSK file path is shown, but its secret content is never sent back
  // by the API -- re-saving without touching this field keeps the same
  // path (and thus the same secret) since the server only re-reads it.
  document.getElementById("f-psk-file").value = cfg.psk_file || "";

  fieldsForTransport(transportSelect.value);
  addModal.style.display = "flex";
}

urlGenerateBtn.addEventListener("click", async () => {
  urlGenerateError.textContent = "";
  urlGenerateBtn.disabled = true;
  const original = urlGenerateBtn.textContent;
  urlGenerateBtn.textContent = "Создаю…";
  try {
    const { url } = await api("/api/yandex-doc", {
      method: "POST",
      body: JSON.stringify({ name: nameInput.value }),
    });
    const existing = urlInput.value.split(",").map((s) => s.trim()).filter(Boolean);
    existing.push(url);
    urlInput.value = existing.join(", ");
  } catch (err) {
    urlGenerateError.textContent = "Не удалось создать документ: " + err.message;
  } finally {
    urlGenerateBtn.disabled = false;
    urlGenerateBtn.textContent = original;
  }
});

addClientBtn.addEventListener("click", openAddModal);
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
    psk_file: document.getElementById("f-psk-file").value,
  };

  try {
    if (editingID) {
      await api(`/api/clients/${encodeURIComponent(editingID)}`, { method: "PUT", body: JSON.stringify(cfg) });
    } else {
      await api("/api/clients", { method: "POST", body: JSON.stringify(cfg) });
    }
    addModal.style.display = "none";
  } catch (err) {
    addError.textContent = err.message;
  } finally {
    // Refresh regardless: a failed-to-start client is still registered
    // (visible with an "error" status), so the operator should see it.
    refreshClients();
  }
});

let qrObjectURL = null;

async function openQRModal(cfg) {
  qrError.textContent = "";
  qrImage.style.display = "none";
  qrModal.style.display = "flex";
  if (qrObjectURL) { URL.revokeObjectURL(qrObjectURL); qrObjectURL = null; }

  try {
    const res = await fetch(`/api/clients/${encodeURIComponent(cfg.id)}/qr`);
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      throw new Error((body && body.error) || `HTTP ${res.status}`);
    }
    const blob = await res.blob();
    qrObjectURL = URL.createObjectURL(blob);
    qrImage.src = qrObjectURL;
    qrImage.style.display = "block";
  } catch (err) {
    qrError.textContent = "Не удалось получить QR: " + err.message;
  }
}

function closeQRModal() {
  qrModal.style.display = "none";
  if (qrObjectURL) { URL.revokeObjectURL(qrObjectURL); qrObjectURL = null; }
}

qrCloseBtn.addEventListener("click", closeQRModal);
qrModal.addEventListener("click", (e) => { if (e.target === qrModal) closeQRModal(); });

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

function formatBytes(bytes) {
  if (!bytes) return "0 Б";
  const units = ["Б", "КБ", "МБ", "ГБ"];
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
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

  const docCount = (cfg.url || "").split(",").map((s) => s.trim()).filter(Boolean).length;

  const meta = document.createElement("div");
  meta.className = "client-meta";
  meta.textContent = `${transportLabel(cfg.transport)} · id: ${cfg.id}`;
  if (docCount > 1) meta.textContent += ` · ${docCount} документа (multi-stream)`;
  if (cfg.psk_file) {
    const badge = document.createElement("span");
    badge.className = "psk-badge";
    badge.textContent = "🔒 PSK";
    badge.title = cfg.psk_file;
    meta.appendChild(badge);
  }
  info.appendChild(meta);

  if (c.status === "running" && c.stats) {
    const stats = document.createElement("div");
    stats.className = "client-stats";
    stats.textContent =
      `аптайм ${formatUptime(c.stats.UptimeSeconds)} · активных: ${c.stats.Established} · ретрансм.: ${c.stats.Retransmits}` +
      ` · ↑${formatBytes(c.bytes_sent)} ↓${formatBytes(c.bytes_received)}`;
    info.appendChild(stats);
  }

  if (c.cupsonline_rooms) {
    const rooms = document.createElement("div");
    rooms.className = "client-rooms";
    rooms.append("URL для клиента (--url): ");
    const code = document.createElement("code");
    code.textContent = c.cupsonline_rooms;
    rooms.appendChild(code);
    info.appendChild(rooms);
  }

  if (c.error) {
    const err = document.createElement("div");
    err.className = "client-error";
    err.textContent = c.error;
    info.appendChild(err);
  }

  div.appendChild(info);

  const actions = document.createElement("div");
  actions.className = "client-actions";

  const qrBtn = document.createElement("button");
  qrBtn.className = "ghost small";
  qrBtn.textContent = "QR";
  qrBtn.addEventListener("click", () => openQRModal(cfg));
  actions.appendChild(qrBtn);

  const editBtn = document.createElement("button");
  editBtn.className = "ghost small";
  editBtn.textContent = "Изменить";
  editBtn.addEventListener("click", () => openEditModal(c));
  actions.appendChild(editBtn);

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
  actions.appendChild(removeBtn);

  div.appendChild(actions);

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
