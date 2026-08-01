import { renderCatalog } from "./catalog.js";
import { renderRoutes } from "./routes.js";

let csrfToken = "";
let sessionReady = false;

const sessionCard = document.querySelector("[data-session-state]");
const statusTitle = document.querySelector("[data-status-title]");
const statusDetail = document.querySelector("[data-status-detail]");
const routeLabel = document.querySelector("[data-route-label]");
const routeOutlet = document.querySelector(".route-outlet");

function setStatus(state, title, detail) {
  sessionCard.dataset.sessionState = state;
  statusTitle.textContent = title;
  statusDetail.textContent = detail;
}

function clearFragment() {
  const clean = `${window.location.pathname}${window.location.search}`;
  window.history.replaceState(null, "", clean);
}

async function bootstrapSession() {
  const fragment = new URLSearchParams(window.location.hash.slice(1));
  let bootstrapToken = fragment.get("token") || "";
  clearFragment();
  routeLabel.textContent = window.location.pathname;
  try {
    const headers = { "Content-Type": "application/json" };
    if (bootstrapToken) headers.Authorization = `Bearer ${bootstrapToken}`;
    const response = await fetch("/api/v1/session", {
      method: "POST",
      credentials: "same-origin",
      headers,
      body: "{}"
    });
    bootstrapToken = "";
    if (!response.ok) throw new Error("bootstrap rejected");
    const receipt = await response.json();
    if (receipt.version !== "memora.admin-session/v1" || !receipt.csrf_token) {
      throw new Error("invalid session receipt");
    }
    csrfToken = receipt.csrf_token;
    sessionReady = true;
    setStatus("ready", "本地只读会话已就绪", "资源已离线加载；所有数据请求都通过受限 MSQL Gateway。 ");
    renderCurrentRoute();
  } catch (_) {
    bootstrapToken = "";
    csrfToken = "";
    sessionReady = false;
    setStatus("error", "无法建立本地会话", "会话验证失败，请关闭本页并重新运行 memora admin。");
  }
}

export async function executeMSQL(source, statements = []) {
  if (!csrfToken) throw new Error("admin session is not ready");
  const response = await fetch("/api/v1/msql", {
    method: "POST",
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "X-Memora-CSRF": csrfToken
    },
    body: JSON.stringify({ source, statements })
  });
  if (response.status === 401) {
    csrfToken = "";
    sessionReady = false;
    setStatus("expired", "本地会话已过期", "请重新运行 memora admin 获取新的临时会话。");
    throw new Error("admin session expired");
  }
  if (!response.ok) throw new Error("admin request failed");
  return response.json();
}

function overviewNode() {
  const fragment = document.createDocumentFragment();
  const title = document.createElement("p");
  title.textContent = "Admin Shell 已离线加载";
  const route = document.createElement("small");
  route.textContent = window.location.pathname;
  fragment.append(title, route);
  return fragment;
}

function updateNavigation(section) {
  for (const link of document.querySelectorAll("[data-nav]")) {
    link.classList.toggle("is-current", link.dataset.nav === section);
  }
}

async function renderCurrentRoute() {
  if (!sessionReady) return;
  const path = window.location.pathname;
  routeLabel.textContent = path;
  if (path === "/catalog" || path.startsWith("/catalog/")) {
    document.body.classList.add("route-catalog");
    document.body.classList.remove("route-routes");
    updateNavigation("catalog");
    await renderCatalog(routeOutlet, {
      path,
      executeMSQL,
      isCurrent: () => window.location.pathname === path
    });
    return;
  }
  if (path === "/routes" || path.startsWith("/routes/")) {
    document.body.classList.remove("route-catalog");
    document.body.classList.add("route-routes");
    updateNavigation("routes");
    await renderRoutes(routeOutlet, {
      path,
      executeMSQL,
      isCurrent: () => window.location.pathname === path
    });
    return;
  }
  document.body.classList.remove("route-catalog");
  document.body.classList.remove("route-routes");
  updateNavigation("overview");
  routeOutlet.dataset.pageState = "ready";
  routeOutlet.replaceChildren(overviewNode());
}

document.addEventListener("click", (event) => {
  const target = event.target instanceof Element ? event.target.closest("a[data-route]") : null;
  if (!target || target.origin !== window.location.origin) return;
  event.preventDefault();
  window.history.pushState(null, "", target.pathname);
  renderCurrentRoute();
});

window.addEventListener("popstate", () => renderCurrentRoute());

bootstrapSession();
