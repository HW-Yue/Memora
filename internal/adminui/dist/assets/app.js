let csrfToken = "";

const sessionCard = document.querySelector("[data-session-state]");
const statusTitle = document.querySelector("[data-status-title]");
const statusDetail = document.querySelector("[data-status-detail]");
const routeLabel = document.querySelector("[data-route-label]");

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
    setStatus("ready", "本地只读会话已就绪", "资源已离线加载；所有数据请求都通过受限 MSQL Gateway。 ");
  } catch (_) {
    bootstrapToken = "";
    csrfToken = "";
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
    setStatus("expired", "本地会话已过期", "请重新运行 memora admin 获取新的临时会话。");
    throw new Error("admin session expired");
  }
  if (!response.ok) throw new Error("admin request failed");
  return response.json();
}

bootstrapSession();
