import type { AppAPI, Dashboard } from "../app/App";
import type { ControlPlaneExportRequest, ControlPlaneImportPreview, ControlPlaneImportRequest, ControlPlaneRecoveryDownload } from "../app/controlPlaneTypes";
import type { AcceptedOperation } from "../app/OperationFeedback";

function announceAccessState(status: number) {
  if (status === 423) window.dispatchEvent(new CustomEvent("shadoc:access-state", { detail: "locked" }));
  if (status === 401 || status === 403) window.dispatchEvent(new CustomEvent("shadoc:access-state", { detail: "unauthorized" }));
}

async function getJSON<T>(path: string): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    announceAccessState(response.status);
    throw new Error(
      response.status === 401
        ? "unauthorized"
        : `request failed: ${response.status}`,
    );
  }
  csrfToken = response.headers.get("X-CSRF-Token") ?? csrfToken;
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

let csrfToken = "";

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
      ...(csrfToken ? { "X-CSRF-Token": csrfToken } : {}),
    },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    announceAccessState(response.status);
    const payload = (await response
      .json()
      .catch(() => ({ error: `请求失败：${response.status}` }))) as {
      error?: string;
    };
    throw new Error(payload.error ?? `请求失败：${response.status}`);
  }
  csrfToken = response.headers.get("X-CSRF-Token") ?? csrfToken;
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

async function postBlob(path: string, body: unknown): Promise<ControlPlaneRecoveryDownload> {
  const response = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: {
      Accept: "application/octet-stream",
      "Content-Type": "application/json",
      ...(csrfToken ? { "X-CSRF-Token": csrfToken } : {}),
    },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    announceAccessState(response.status);
    const payload = await response.json().catch(() => ({ error: `请求失败：${response.status}` })) as { error?: string };
    throw new Error(payload.error ?? `请求失败：${response.status}`);
  }
  csrfToken = response.headers.get("X-CSRF-Token") ?? csrfToken;
  const candidate = response.headers.get("Content-Disposition")?.match(/filename="([^"]+)"/)?.[1] ?? "shadoc-recovery.rcbundle";
  const filename = /^[A-Za-z0-9._-]+$/.test(candidate) ? candidate : "shadoc-recovery.rcbundle";
  return { blob: await response.blob(), filename };
}

async function getBlob(path: string, fallbackFilename: string): Promise<{ blob: Blob; filename: string }> {
  const response = await fetch(path, {
    credentials: "same-origin",
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    announceAccessState(response.status);
    const payload = await response.json().catch(() => ({ error: `请求失败：${response.status}` })) as { error?: string };
    throw new Error(payload.error ?? `请求失败：${response.status}`);
  }
  csrfToken = response.headers.get("X-CSRF-Token") ?? csrfToken;
  const candidate = response.headers.get("Content-Disposition")?.match(/filename="([^"]+)"/)?.[1] ?? fallbackFilename;
  const filename = /^[A-Za-z0-9._-]+$/.test(candidate) ? candidate : fallbackFilename;
  return { blob: await response.blob(), filename };
}

async function postMultipart<T>(path: string, body: FormData): Promise<T> {
  const response = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      ...(csrfToken ? { "X-CSRF-Token": csrfToken } : {}),
    },
    body,
  });
  if (!response.ok) {
    announceAccessState(response.status);
    const payload = await response.json().catch(() => ({ error: `请求失败：${response.status}` })) as { error?: string };
    throw new Error(payload.error ?? `请求失败：${response.status}`);
  }
  csrfToken = response.headers.get("X-CSRF-Token") ?? csrfToken;
  return response.json() as Promise<T>;
}

async function mutateJSON(
  path: string,
  method: "POST" | "PUT" | "DELETE",
  body?: unknown,
): Promise<void> {
  const response = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(csrfToken ? { "X-CSRF-Token": csrfToken } : {}),
    },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  if (!response.ok) {
    announceAccessState(response.status);
    const payload = (await response
      .json()
      .catch(() => ({ error: `请求失败：${response.status}` }))) as {
      error?: string;
    };
    throw new Error(payload.error ?? `请求失败：${response.status}`);
  }
}

async function putJSON<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(path, {
    method: "PUT",
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
      ...(csrfToken ? { "X-CSRF-Token": csrfToken } : {}),
    },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    announceAccessState(response.status);
    const payload = await response.json().catch(() => ({ error: `请求失败：${response.status}` })) as { error?: string };
    throw new Error(payload.error ?? `请求失败：${response.status}`);
  }
  csrfToken = response.headers.get("X-CSRF-Token") ?? csrfToken;
  return response.json() as Promise<T>;
}

async function getText(path: string): Promise<string> {
  const response = await fetch(path, { credentials: "same-origin", headers: { Accept: "text/plain" } });
  if (!response.ok) {
    announceAccessState(response.status);
    const payload = await response.json().catch(() => ({ error: `请求失败：${response.status}` })) as { error?: string };
    throw new Error(payload.error ?? `请求失败：${response.status}`);
  }
  return response.text();
}

export const httpAPI: AppAPI = {
  setupStatus: () => getJSON<{ initialized: boolean }>("/api/setup/status"),
  setup: (username, password, token) =>
    postJSON<{ username: string }>("/api/setup", { username, password, token }),
  login: (username, password) =>
    postJSON<{ username: string }>("/api/login", { username, password }),
  session: () => getJSON<{ username: string }>("/api/session"),
  async logout() {
    await mutateJSON("/api/logout", "POST");
    csrfToken = "";
  },
  vaultStatus: () =>
    getJSON<{
      mode: "automatic" | "lock-on-restart";
      locked: boolean;
    }>("/api/vault/status"),
  unlockVault: (passphrase) =>
    mutateJSON("/api/vault/unlock", "POST", { passphrase }),
  setVaultLockOnRestart: (passphrase) =>
    mutateJSON("/api/vault/lock-on-restart", "POST", { passphrase }),
  setVaultAutomatic: (password, confirmed) => mutateJSON("/api/vault/automatic", "POST", { password, confirmed }),
  exportControlPlane: (payload: ControlPlaneExportRequest) => postBlob("/api/control-plane/export", payload),
  preflightControlPlaneImport(bundle: File, recoveryPassphrase: string) {
    const body = new FormData();
    body.append("bundle", bundle);
    body.append("recoveryPassphrase", recoveryPassphrase);
    return postMultipart<ControlPlaneImportPreview>("/api/control-plane/import/preflight", body);
  },
  importControlPlane(bundle: File, payload: ControlPlaneImportRequest) {
    const body = new FormData();
    body.append("bundle", bundle);
    body.append("recoveryPassphrase", payload.recoveryPassphrase);
    body.append("previewId", payload.previewId);
    body.append("administratorPassword", payload.administratorPassword);
    body.append("impactConfirmed", String(payload.impactConfirmed));
    return postMultipart<AcceptedOperation>("/api/control-plane/import", body);
  },
  agentServiceStatus: () => getJSON("/api/agent-service"),
  saveAgentServiceSettings: (settings) => putJSON("/api/agent-service", settings),
  lifecyclePolicy: () => getJSON("/api/lifecycle-policy"),
  saveLifecyclePolicy: (policy) =>
    mutateJSON("/api/lifecycle-policy", "PUT", policy),
  previewLifecycleCleanup: () => postJSON("/api/lifecycle/cleanup/preview", {}),
  cleanupLifecycle: (password) => postJSON("/api/lifecycle/cleanup", { password }),
  applicationVersion: () => getJSON("/api/application/version"),
  applicationReleases: () => getJSON("/api/application/releases"),
  exportDiagnostics: () => getBlob("/api/diagnostics/export", "shadoc-diagnostics.json"),
  async dashboard(): Promise<Dashboard> {
    return getJSON<Dashboard>("/api/dashboard");
  },
  compatibility: () => getJSON("/api/compatibility"),
  async runTask(taskId: string) {
    await postJSON(`/api/tasks/${encodeURIComponent(taskId)}/run`, {});
  },
  listResource: (resource: string) => getJSON(`/api/${resource}`),
  async createResource(resource: string, payload: Record<string, unknown>) {
    return postJSON(`/api/${resource}`, payload);
  },
  updateResource: (resource, id, payload) =>
    mutateJSON(`/api/${resource}/${encodeURIComponent(id)}`, "PUT", payload),
  deleteResource: (resource, id) =>
    mutateJSON(`/api/${resource}/${encodeURIComponent(id)}`, "DELETE"),
  runDetail: (id) => getJSON(`/api/runs/${encodeURIComponent(id)}`),
  runLog: (id) => getText(`/api/runs/${encodeURIComponent(id)}/log`),
  saveMaintenance: (id, payload) =>
    mutateJSON(
      `/api/repositories/${encodeURIComponent(id)}/maintenance-policy`,
      "PUT",
      payload,
    ),
  saveRepositoryCapacityPolicy: (id, payload) =>
    putJSON(`/api/repositories/${encodeURIComponent(id)}/capacity-policy`, payload),
  saveRestoreVerificationPolicy: (taskId, payload) =>
    putJSON(`/api/tasks/${encodeURIComponent(taskId)}/restore-verification-policy`, payload),
  deleteRestoreVerificationPolicy: (taskId) =>
    mutateJSON(`/api/tasks/${encodeURIComponent(taskId)}/restore-verification-policy`, "DELETE"),
  action: (path: string, payload?: Record<string, unknown>) =>
    payload === undefined ? getJSON(path) : postJSON(path, payload),
};
