import { afterEach, describe, expect, it, vi } from "vitest";
import { httpAPI } from "./client";

describe("HTTP API client", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("accepts a successful POST with an empty 204 response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(httpAPI.action("/api/example/confirm", { confirmed: true })).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/example/confirm",
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("downloads a recovery bundle with CSRF protection and a safe filename", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ username: "admin" }), {
        status: 200,
        headers: { "Content-Type": "application/json", "X-CSRF-Token": "csrf-recovery" },
      }))
      .mockResolvedValueOnce(new Response("sealed recovery bundle", {
        status: 200,
        headers: {
          "Content-Type": "application/octet-stream",
          "Content-Disposition": 'attachment; filename="shadoc-recovery-20260715T120000Z.rcbundle"',
        },
      }));
    vi.stubGlobal("fetch", fetchMock);
    await httpAPI.session();

    const result = await httpAPI.exportControlPlane({
      administratorPassword: "admin-password",
      recoveryPassphrase: "recovery-passphrase",
      recoveryPassphraseConfirmation: "recovery-passphrase",
    });

    expect(result.filename).toBe("shadoc-recovery-20260715T120000Z.rcbundle");
    await expect(blobText(result.blob)).resolves.toBe("sealed recovery bundle");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/control-plane/export", expect.objectContaining({
      method: "POST",
      credentials: "same-origin",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-recovery" }),
      body: JSON.stringify({
        administratorPassword: "admin-password",
        recoveryPassphrase: "recovery-passphrase",
        recoveryPassphraseConfirmation: "recovery-passphrase",
      }),
    }));
  });

  it("downloads a diagnostic bundle with a read-only request and a safe filename", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("redacted diagnostics", {
      status: 200,
      headers: {
        "Content-Type": "application/json",
        "Content-Disposition": 'attachment; filename="shadoc-diagnostics-20260715T120000Z.json"',
      },
    }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await httpAPI.exportDiagnostics();

    expect(result.filename).toBe("shadoc-diagnostics-20260715T120000Z.json");
    await expect(blobText(result.blob)).resolves.toBe("redacted diagnostics");
    expect(fetchMock).toHaveBeenCalledWith("/api/diagnostics/export", {
      credentials: "same-origin",
      headers: { Accept: "application/json" },
    });
  });

  it("uploads recovery preflight and import as multipart without setting a content type", async () => {
    const preview = {
      previewId: "preview-1", canImport: true, sourceApplicationVersion: "1.2.3", resourceCounts: {}, conflicts: [], missingTools: [], revalidation: [], excludedTransientClasses: [], restartRequired: false, warnings: [],
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ username: "admin" }), {
        status: 200,
        headers: { "Content-Type": "application/json", "X-CSRF-Token": "csrf-recovery" },
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify(preview), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ operationId: "op-1", status: "queued" }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await httpAPI.session();
    const bundle = new File(["sealed"], "recovery.rcbundle", { type: "application/octet-stream" });

    await expect(httpAPI.preflightControlPlaneImport(bundle, "recovery-passphrase")).resolves.toEqual(preview);
    await expect(httpAPI.importControlPlane(bundle, {
      recoveryPassphrase: "recovery-passphrase",
      previewId: "preview-1",
      administratorPassword: "admin-password",
      impactConfirmed: true,
    })).resolves.toEqual({ operationId: "op-1", status: "queued" });

    const preflightRequest = fetchMock.mock.calls[1][1] as RequestInit;
    expect(preflightRequest.headers).toEqual(expect.objectContaining({ "X-CSRF-Token": "csrf-recovery" }));
    expect(preflightRequest.headers).not.toHaveProperty("Content-Type");
    const preflightBody = preflightRequest.body as FormData;
    expect(preflightBody.get("bundle")).toBe(bundle);
    expect(preflightBody.get("recoveryPassphrase")).toBe("recovery-passphrase");

    const importRequest = fetchMock.mock.calls[2][1] as RequestInit;
    expect(importRequest.headers).not.toHaveProperty("Content-Type");
    const importBody = importRequest.body as FormData;
    expect(importBody.get("bundle")).toBe(bundle);
    expect(importBody.get("previewId")).toBe("preview-1");
    expect(importBody.get("administratorPassword")).toBe("admin-password");
    expect(importBody.get("impactConfirmed")).toBe("true");
  });
});

function blobText(blob: Blob): Promise<string> {
  if (typeof blob.text === "function") {
    return blob.text();
  }
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(reader.error);
    reader.onload = () => resolve(String(reader.result));
    reader.readAsText(blob);
  });
}
