import { useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SnapshotBrowser, SnapshotDiffPanel } from "./SnapshotBrowser";

const snapshots = [
  { id: "old", time: "2026-07-10T01:00:00Z", paths: ["/srv/photos"] },
  { id: "current", time: "2026-07-11T01:00:00Z", paths: ["/srv/photos"] },
];

function Harness({ action }: { action: (path: string) => Promise<unknown> }) {
  const [selected, setSelected] = useState<string[]>([]);
  return <>
    <SnapshotBrowser
      api={{ action }}
      repositoryID="repo"
      snapshotID="current"
      sourcePath="/srv/photos"
      snapshots={snapshots}
      selectedIncludes={selected}
      onSelectedIncludesChange={setSelected}
      locale="zh-CN"
    />
    <output aria-label="已选路径">{selected.join(",")}</output>
  </>;
}

describe("SnapshotBrowser", () => {
  it("loads the snapshot root, paginates explicitly, and preserves relative selections", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (requestPath: string) => {
      const url = new URL(requestPath, "http://localhost");
      if (url.pathname.endsWith("/contents") && url.searchParams.get("cursor") === "next-root") {
        return { items: [{ name: "root.txt", type: "file", path: "/srv/photos/root.txt", size: 3 }], path: "/srv/photos", recursive: false, truncated: false };
      }
      return { items: [{ name: "album", type: "dir", path: "/srv/photos/album" }], path: "/srv/photos", recursive: false, truncated: true, nextCursor: "next-root" };
    });
    render(<Harness action={action} />);

    expect(await screen.findByText("album")).toBeVisible();
    expect(screen.queryByRole("button", { name: "打开目录 album" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("搜索快照路径")).not.toBeInTheDocument();
    expect(screen.getByText("还有更多项目，当前列表不是完整结果。")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "加载更多" }));
    expect(await screen.findByText("root.txt")).toBeVisible();
    await user.click(screen.getByRole("checkbox", { name: "选择 /srv/photos/album" }));
    expect(screen.getByLabelText("已选路径")).toHaveTextContent("album");
    expect(action.mock.calls.some(([path]) => String(path).includes("path=%2Fsrv%2Fphotos"))).toBe(true);
    const callsBeforeReload = action.mock.calls.length;
    await user.click(screen.getByRole("button", { name: "重新加载" }));
    await vi.waitFor(() => expect(action).toHaveBeenCalledTimes(callsBeforeReload + 1));
  });

  it("shows complete diff counts separately from bounded examples", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (requestPath: string) => {
      if (requestPath.includes("snapshot-diff")) {
        return { fromSnapshotId: "old", toSnapshotId: "current", added: 12, modified: 3, removed: 2, items: [{ path: "/srv/photos/new.jpg", change: "added", type: "file", size: 9 }], examplesTruncated: true, incomplete: false };
      }
      return { items: [], path: "/srv/photos", recursive: false, truncated: false };
    });
    render(<SnapshotDiffPanel api={{ action }} repositoryID="repo" snapshotID="current" sourcePath="/srv/photos" snapshots={snapshots} locale="zh-CN" />);

    const comparison = await screen.findByRole("region", { name: "快照差异" });
    await user.selectOptions(within(comparison).getByLabelText("对比基线快照"), "old");
    await user.click(within(comparison).getByRole("button", { name: "比较快照" }));
    expect(await within(comparison).findByText("新增 12 · 修改 3 · 删除 2")).toBeVisible();
    expect(within(comparison).getByText("变更样例已截断，但上方计数来自完整比较。")).toBeVisible();
    expect(within(comparison).getByText("/srv/photos/new.jpg")).toBeVisible();
  });
});
