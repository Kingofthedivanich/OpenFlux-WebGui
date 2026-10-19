const $ = (id) => document.getElementById(id);

const loginView = $("login-view");
const dashboardView = $("dashboard-view");
const loginForm = $("login-form");
const loginError = $("login-error");

const addModal = $("add-modal");
const addForm = $("add-form");
const addError = $("add-error");
const modalTitle = $("add-modal-title");
const submitBtn = $("add-submit-btn");
const nameInput = $("f-name");
const transportsBox = $("f-transports");
const generateError = $("f-generate-error");
const directPortInput = $("f-direct-port");
const modeSelect = $("f-mode");

const clientsList = $("clients-list");
const clientsEmpty = $("clients-empty");

const qrModal = $("qr-modal");
const qrImage = $("qr-image");
const qrLink = $("qr-link");
const qrError = $("qr-error");

const logsModal = $("logs-modal");
const logsBody = $("logs-body");

const yandexTokenCard = $("yandex-token-card");
const yandexTokenStatus = $("yandex-token-status");
const yandexTokenInput = $("yandex-token-input");
const yandexTokenError = $("yandex-token-error");

const TRANSPORT_LABELS = {
  vyandex: "Yandex Docs (Volga)",
  yandex: "Yandex Docs",
  mailru: "Mail.ru Docs",
  telemost: "Yandex Telemost",
  boards: "Yandex Boards",
  avito: "Avito Code",
  cupsonline: "Cups.online",
};
const MAKES_OWN_ROOMS = new Set(["cupsonline", "avito"]);
const URL_PLACEHOLDERS = {
  vyandex: "https://disk.yandex.ru/edit/d/...",
  yandex: "https://disk.yandex.ru/i/...",
  mailru: "https://cloud.mail.ru/public/...",
  telemost: "https://telemost.yandex.ru/j/...",
  boards: "https://yandex.ru/boards/...",
};

let transportTypes = Object.keys(TRANSPORT_LABELS);
let pollTimer = null;
let editingID = null; // null: the modal adds a client
let yandexDocAvailable = false;
let logsClientID = null;

async function api(path, opts) {
  const res = await fetch(path, { headers: { "Content-Type": "application/json" }, ...opts });
  let body = null;
  try { body = await res.json(); } catch (_) {}
  if (!res.ok) {
    const err = new Error((body && body.error) || `HTTP ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return body;
}

// ---- session ----

function showDashboard() {
  loginView.style.display = "none";
  dashboardView.style.display = "block";
  api("/api/transport-types").then((t) => { if (t && t.length) transportTypes = t; }).catch(() => {});
  loadYandexDocAvailability();
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
    s.authenticated ? showDashboard() : showLogin();
  } catch (_) {
    showLogin();
  }
}

loginForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  loginError.textContent = "";
  try {
    await api("/api/login", {
      method: "POST",
      body: JSON.stringify({ username: $("login-user").value, password: $("login-pass").value }),
    });
    showDashboard();
  } catch (_) {
    loginError.textContent = "Неверный логин или пароль";
  }
});

$("logout-btn").addEventListener("click", async () => {
  await api("/api/logout", { method: "POST" });
  showLogin();
});

// ---- Yandex token ----

async function loadYandexDocAvailability() {
  try {
    const { available, configured } = await api("/api/yandex-doc-available");
    yandexDocAvailable = !!available;
    yandexTokenCard.style.display = configured ? "block" : "none";
    yandexTokenStatus.textContent = available
      ? "Токен настроен. Вставь новый, чтобы заменить."
      : "Токен не задан — документы Volga можно только вставлять вручную.";
  } catch (_) {
    yandexDocAvailable = false;
    yandexTokenCard.style.display = "none";
  }
  transportsBox.querySelectorAll(".transport-row").forEach(updateRow);
}

$("yandex-token-save").addEventListener("click", async (e) => {
  yandexTokenError.textContent = "";
  const token = yandexTokenInput.value.trim();
  if (!token) { yandexTokenError.textContent = "Вставь токен перед сохранением."; return; }
  e.target.disabled = true;
  try {
    await api("/api/yandex-token", { method: "PUT", body: JSON.stringify({ token }) });
    yandexTokenInput.value = "";
    await loadYandexDocAvailability();
  } catch (err) {
    yandexTokenError.textContent = "Не удалось сохранить: " + err.message;
  } finally {
    e.target.disabled = false;
  }
});

$("yandex-token-clear").addEventListener("click", async (e) => {
  yandexTokenError.textContent = "";
  if (!confirm("Отключить генерацию документов и удалить сохранённый токен?")) return;
  e.target.disabled = true;
  try {
    await api("/api/yandex-token", { method: "DELETE" });
    await loadYandexDocAvailability();
  } catch (err) {
    yandexTokenError.textContent = "Не удалось отключить: " + err.message;
  } finally {
    e.target.disabled = false;
  }
});

// ---- add / edit form ----

function addTransportRow(t) {
  const row = document.createElement("div");
  row.className = "transport-row";

  const select = document.createElement("select");
  for (const type of transportTypes) {
    const opt = document.createElement("option");
    opt.value = type;
    opt.textContent = TRANSPORT_LABELS[type] || type;
    select.appendChild(opt);
  }
  select.value = (t && t.type) || transportTypes[0];

  const url = document.createElement("input");
  url.type = "text";
  url.value = (t && t.url) || "";

  const gen = document.createElement("button");
  gen.type = "button";
  gen.className = "ghost small";
  gen.textContent = "Создать";
  gen.title = "Создать документ на Яндекс Диске";
  gen.addEventListener("click", () => generateDoc(url, gen));

  const del = document.createElement("button");
  del.type = "button";
  del.className = "ghost small";
  del.textContent = "✕";
  del.title = "Убрать транспорт";
  del.addEventListener("click", () => row.remove());

  // The rooms a cupsonline/avito exit made ride in the hidden URL field, so
  // an edit keeps them (and the link handed out); another type drops them.
  row.dataset.type = select.value;
  select.addEventListener("change", () => {
    if (MAKES_OWN_ROOMS.has(row.dataset.type) || MAKES_OWN_ROOMS.has(select.value)) url.value = "";
    row.dataset.type = select.value;
    updateRow(row);
  });
  row.append(select, url, gen, del);
  transportsBox.appendChild(row);
  updateRow(row);
}

// updateRow shows the URL field and the "create" button the type needs.
function updateRow(row) {
  const [select, url, gen] = row.children;
  const type = select.value;
  const ownRooms = MAKES_OWN_ROOMS.has(type);
  url.style.display = ownRooms ? "none" : "";
  url.placeholder = URL_PLACEHOLDERS[type] || "ссылка";
  // A .docx uploaded through the Disk API always opens in the Volga editor,
  // which only the vyandex transport speaks.
  gen.style.display = yandexDocAvailable && type === "vyandex" ? "" : "none";
}

async function generateDoc(urlInput, btn) {
  generateError.textContent = "";
  btn.disabled = true;
  const original = btn.textContent;
  btn.textContent = "…";
  try {
    const { url } = await api("/api/yandex-doc", { method: "POST", body: JSON.stringify({ name: nameInput.value }) });
    urlInput.value = url;
  } catch (err) {
    generateError.textContent = "Не удалось создать документ: " + err.message;
  } finally {
    btn.disabled = false;
    btn.textContent = original;
  }
}

$("f-add-transport").addEventListener("click", () => addTransportRow(null));

function openModal(status) {
  addForm.reset();
  addError.textContent = "";
  generateError.textContent = "";
  transportsBox.innerHTML = "";
  if (status) {
    const cfg = status.config;
    editingID = cfg.id;
    modalTitle.textContent = "Редактировать клиента";
    submitBtn.textContent = "Сохранить";
    nameInput.value = cfg.name || "";
    (cfg.transports || []).forEach(addTransportRow);
    directPortInput.value = cfg.direct_port || "";
    modeSelect.value = cfg.mode || "l4";
  } else {
    editingID = null;
    modalTitle.textContent = "Новый клиент";
    submitBtn.textContent = "Добавить";
    addTransportRow(null);
  }
  addModal.style.display = "flex";
}

$("add-client-btn").addEventListener("click", () => openModal(null));
$("add-cancel-btn").addEventListener("click", () => { addModal.style.display = "none"; });
addModal.addEventListener("click", (e) => { if (e.target === addModal) addModal.style.display = "none"; });

addForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  addError.textContent = "";
  const transports = [...transportsBox.querySelectorAll(".transport-row")].map((row) => {
    const [select, url] = row.children;
    const value = url.value.trim();
    return value ? { type: select.value, url: value } : { type: select.value };
  });
  const cfg = {
    name: nameInput.value.trim(),
    transports,
    mode: modeSelect.value,
    direct_port: parseInt(directPortInput.value, 10) || 0,
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
    // A client whose channel did not start is still listed with its error.
    refreshClients();
  }
});

// ---- link / QR ----

let qrObjectURL = null;

async function openQRModal(c) {
  qrError.textContent = "";
  qrImage.style.display = "none";
  qrLink.textContent = c.link || "";
  qrModal.style.display = "flex";
  if (qrObjectURL) { URL.revokeObjectURL(qrObjectURL); qrObjectURL = null; }
  try {
    const res = await fetch(`/api/clients/${encodeURIComponent(c.config.id)}/qr`);
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      throw new Error((body && body.error) || `HTTP ${res.status}`);
    }
    qrObjectURL = URL.createObjectURL(await res.blob());
    qrImage.src = qrObjectURL;
    qrImage.style.display = "block";
  } catch (err) {
    qrError.textContent = "Нет QR: " + err.message;
  }
}

function closeQRModal() {
  qrModal.style.display = "none";
  qrLink.textContent = "";
  if (qrObjectURL) { URL.revokeObjectURL(qrObjectURL); qrObjectURL = null; }
}

$("qr-close-btn").addEventListener("click", closeQRModal);
qrModal.addEventListener("click", (e) => { if (e.target === qrModal) closeQRModal(); });
$("qr-copy-btn").addEventListener("click", (e) => copyText(qrLink, e.target));

async function copyText(el, btn) {
  try {
    await navigator.clipboard.writeText(el.textContent);
    const original = btn.textContent;
    btn.textContent = "Скопировано";
    setTimeout(() => { btn.textContent = original; }, 1500);
  } catch (_) {
    // No clipboard API outside a secure context: select it for a manual copy.
    const range = document.createRange();
    range.selectNodeContents(el);
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
  }
}

// ---- logs ----

async function loadLogs() {
  if (!logsClientID) return;
  try {
    const { lines } = await api(`/api/clients/${encodeURIComponent(logsClientID)}/logs`);
    logsBody.textContent = (lines || []).join("\n") || "(пусто)";
    logsBody.scrollTop = logsBody.scrollHeight;
  } catch (err) {
    logsBody.textContent = "Не удалось загрузить: " + err.message;
  }
}

function openLogsModal(c) {
  logsClientID = c.config.id;
  $("logs-modal-title").textContent = "Логи — " + (c.config.name || c.config.id);
  logsBody.textContent = "…";
  logsModal.style.display = "flex";
  loadLogs();
}

function closeLogsModal() {
  logsModal.style.display = "none";
  logsClientID = null;
}

$("logs-refresh-btn").addEventListener("click", loadLogs);
$("logs-close-btn").addEventListener("click", closeLogsModal);
logsModal.addEventListener("click", (e) => { if (e.target === logsModal) closeLogsModal(); });

// ---- client list ----

function formatUptime(startedAt) {
  const seconds = (Date.now() - Date.parse(startedAt)) / 1000;
  if (!startedAt || !(seconds > 0)) return "0с";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) return `${h}ч ${m}м`;
  if (m > 0) return `${m}м ${s}с`;
  return `${s}с`;
}

function formatBytes(bytes) {
  if (!bytes) return "0 Б";
  const units = ["Б", "КБ", "МБ", "ГБ", "ТБ"];
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

const STATUS_TEXT = {
  starting: "запускается",
  running: "работает",
  restarting: "перезапуск",
  stopped: "остановлен",
  error: "ошибка",
};

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function button(text, cls, onClick) {
  const b = el("button", cls, text);
  b.type = "button";
  b.addEventListener("click", onClick);
  return b;
}

function renderClient(c) {
  const cfg = c.config;
  const card = el("div", "client-card");

  let dotClass = "error";
  if (c.connected) dotClass = "running";
  else if (c.status === "running" || c.status === "starting") dotClass = "waiting";
  card.appendChild(el("div", "status-dot " + dotClass));

  const info = el("div", "client-info");
  info.appendChild(el("div", "client-name", cfg.name || cfg.id));

  const types = (cfg.transports || []).map((t) => TRANSPORT_LABELS[t.type] || t.type);
  if (cfg.direct_port) types.push(`direct :${cfg.direct_port}`);
  info.appendChild(el("div", "client-meta", `${types.join(" · ")} · ${cfg.mode || "l4"} · id ${cfg.id}`));

  let line = STATUS_TEXT[c.status] || c.status;
  if (c.status === "running") {
    line += c.connected ? " · клиент на связи" : " · ждёт клиента";
    line += ` · аптайм ${formatUptime(c.started_at)} · ↑${formatBytes(c.bytes_out)} ↓${formatBytes(c.bytes_in)}`;
    if (c.connected && c.active && c.active.length) line += ` · через ${c.active.join(", ")}`;
  }
  if (c.restarts) line += ` · перезапусков: ${c.restarts}`;
  info.appendChild(el("div", "client-stats", line));

  if (c.check) {
    const check = el("div", "client-error", `${c.check.transport}: нужна проверка/вход (${c.check.reason || "captcha"})`);
    if (c.check.url) {
      check.append(" ");
      const a = el("a", "", "открыть");
      a.href = c.check.url;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      check.appendChild(a);
    }
    info.appendChild(check);
  }
  if (c.error) info.appendChild(el("div", "client-error", c.error));
  card.appendChild(info);

  const actions = el("div", "client-actions");
  const linkBtn = button("Ссылка / QR", "ghost small", () => openQRModal(c));
  linkBtn.disabled = !c.link;
  if (!c.link) linkBtn.title = "Канал ещё не напечатал ссылку";
  actions.append(
    linkBtn,
    button("Логи", "ghost small", () => openLogsModal(c)),
    button("Изменить", "ghost small", () => openModal(c)),
    button("Удалить", "remove-btn", async () => {
      if (!confirm(`Удалить клиента «${cfg.name || cfg.id}»? Его ключ будет удалён, ссылка перестанет работать.`)) return;
      try {
        await api(`/api/clients/${encodeURIComponent(cfg.id)}`, { method: "DELETE" });
        refreshClients();
      } catch (err) {
        alert("Не удалось удалить: " + err.message);
      }
    }),
  );
  card.appendChild(actions);
  return card;
}

async function refreshClients() {
  let list;
  try {
    list = await api("/api/clients");
  } catch (err) {
    if (err.status === 401) showLogin();
    return;
  }
  clientsList.innerHTML = "";
  if (!list || list.length === 0) {
    clientsEmpty.style.display = "block";
    return;
  }
  clientsEmpty.style.display = "none";
  list.forEach((c) => clientsList.appendChild(renderClient(c)));
}

checkSession();
