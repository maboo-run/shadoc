import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AgentServiceSettings } from "./AgentServiceSettings";

describe("Agent Service endpoint migration preview", () => {
  it("lists affected Agents and requires acknowledgement before changing the URL", async () => {
    const user = userEvent.setup();
    const saveAgentServiceSettings = vi.fn(async (settings: { enabled: boolean; port: number; advertisedHost: string }) => ({
      ...settings, running: true, listenAddress: `0.0.0.0:${settings.port}`, serviceUrl: `https://${settings.advertisedHost}:${settings.port}`,
    }));
    const api = {
      agentServiceStatus: async () => ({ enabled: true, running: true, port: 9443, advertisedHost: "old.internal", listenAddress: "0.0.0.0:9443", serviceUrl: "https://old.internal:9443" }),
      saveAgentServiceSettings,
      listResource: async () => [
        { id: "managed-a", remoteHostId: "host-a", status: "online", serviceUrl: "https://old.internal:9443" },
        { id: "manual-b", status: "online", serviceUrl: "https://old.internal:9443" },
      ],
    };
    render(<AgentServiceSettings api={api} locale="zh-CN" onStatus={() => undefined} onMessage={() => undefined} />);

    const host = await screen.findByLabelText("控制服务访问地址（IP 或域名）");
    await user.clear(host);
    await user.type(host, "new.internal");
    expect(screen.getByRole("heading", { name: "此变更会影响 2 个 Agent" })).toBeVisible();
    expect(screen.getByText(/managed-a.*托管迁移/)).toBeVisible();
    expect(screen.getByText(/manual-b.*手工迁移/)).toBeVisible();
    expect(screen.getByRole("button", { name: "保存并应用" })).toBeDisabled();

    await user.click(screen.getByLabelText("我已记录受影响 Agent，并会逐个完成地址迁移"));
    await user.click(screen.getByRole("button", { name: "保存并应用" }));
    expect(saveAgentServiceSettings).toHaveBeenCalledWith({ enabled: true, port: 9443, advertisedHost: "new.internal" });
  });
});
