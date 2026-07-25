import { useEffect, useMemo, useRef, useState } from "react";
import { translate, type Locale } from "../i18n";
import type { AppAPI } from "./App";
import { navigation } from "./Sidebar";
import { useModalFocus } from "./useModalFocus";

type ResultGroup = "page" | "task" | "repository" | "agent";
type Result = { id: string; label: string; group: ResultGroup; page?: string };

type CommandPaletteProps = {
  api: AppAPI;
  locale: Locale;
  onClose(): void;
  onNavigate(page: string): void;
};

const groupLabels: Record<ResultGroup, (t: (s: string) => string) => string> = {
  page: (t) => t("页面"),
  task: (t) => t("任务"),
  repository: (t) => t("仓库"),
  agent: (t) => t("Agent 节点"),
};

const pageRoutes: Record<string, string> = {
  "仪表盘": "仪表盘",
  "兼容性中心": "兼容性中心",
  "远程主机": "远程主机",
  "Agent 节点": "Agent 节点",
  "备份仓库": "备份仓库",
  "数据库实例": "数据库实例",
  "备份任务": "备份任务",
  "快照与恢复": "快照与恢复",
  "运行记录": "运行记录",
  "告警历史": "告警历史",
  "投递记录": "投递记录",
  "审计日志": "审计日志",
  "通知配置": "通知配置",
  "Agent 服务": "Agent 服务",
  "安全设置": "安全设置",
  "配置备份与恢复": "配置备份与恢复",
  "数据生命周期": "数据生命周期",
  "界面语言": "界面语言",
};

export function CommandPalette({ api, locale, onClose, onNavigate }: CommandPaletteProps) {
  const t = (source: string) => translate(locale, source);
  const [query, setQuery] = useState("");
  const [tasks, setTasks] = useState<Array<{ id: string; name: string }>>([]);
  const [repositories, setRepositories] = useState<Array<{ id: string; name: string }>>([]);
  const [agents, setAgents] = useState<Array<{ id: string; status?: string }>>([]);
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  useEffect(() => {
    let cancelled = false;
    Promise.all([
      api.listResource("tasks").catch(() => []),
      api.listResource("repositories").catch(() => []),
      api.listResource("agents").catch(() => []),
    ]).then(([tasksData, reposData, agentsData]) => {
      if (cancelled) return;
      setTasks(tasksData as Array<{ id: string; name: string }>);
      setRepositories(reposData as Array<{ id: string; name: string }>);
      setAgents(agentsData as Array<{ id: string; status?: string }>);
    });
    return () => { cancelled = true; };
  }, [api]);

  const results = useMemo<Result[]>(() => {
    const q = query.trim().toLowerCase();
    const all: Result[] = [
      ...navigation.map((page) => ({ id: `page:${page}`, label: page, group: "page" as const, page: pageRoutes[page] ?? page })),
      ...tasks.map((task) => ({ id: `task:${task.id}`, label: task.name, group: "task" as const, page: "备份任务" })),
      ...repositories.map((repo) => ({ id: `repo:${repo.id}`, label: repo.name, group: "repository" as const, page: "备份仓库" })),
      ...agents.map((agent) => ({ id: `agent:${agent.id}`, label: agent.id, group: "agent" as const, page: "Agent 节点" })),
    ];
    if (!q) return all.slice(0, 8);
    return all.filter((item) => item.label.toLowerCase().includes(q)).slice(0, 20);
  }, [query, tasks, repositories, agents]);

  useEffect(() => {
    setActiveIndex(0);
  }, [query]);

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.key === "ArrowDown") { event.preventDefault(); setActiveIndex((i) => Math.min(i + 1, results.length - 1)); }
      if (event.key === "ArrowUp") { event.preventDefault(); setActiveIndex((i) => Math.max(i - 1, 0)); }
      if (event.key === "Enter") {
        event.preventDefault();
        const selected = results[activeIndex];
        if (selected?.page) { onNavigate(selected.page); }
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [results, activeIndex, onNavigate]);

  useModalFocus(dialogRef, onClose);

  const grouped = useMemo(() => {
    const map = new Map<ResultGroup, Result[]>();
    for (const r of results) {
      const list = map.get(r.group) ?? [];
      list.push(r);
      map.set(r.group, list);
    }
    return map;
  }, [results]);

  let flatIndex = -1;

  return (
    <div className="cmd-palette-backdrop" onClick={onClose}>
      <div
        ref={dialogRef}
        className="cmd-palette-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={t("命令面板")}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="cmd-palette-input-row">
          <span className="cmd-prompt" aria-hidden="true">$</span>
          <input
            ref={inputRef}
            type="text"
            className="cmd-palette-input"
            placeholder={t("搜索页面、任务、仓库…")}
            aria-label={t("搜索页面、任务、仓库…")}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
        <div className="cmd-palette-results" role="listbox" aria-label={t("搜索结果")}>
          {results.length === 0 && <p className="cmd-palette-empty">{t("无匹配结果")}</p>}
          {(["page", "task", "repository", "agent"] as ResultGroup[]).map((group) => {
            const items = grouped.get(group);
            if (!items?.length) return null;
            return (
              <div className="cmd-palette-group" key={group}>
                <div className="cmd-palette-group-label">{groupLabels[group](t)}</div>
                {items.map((item) => {
                  flatIndex += 1;
                  const idx = flatIndex;
                  return (
                    <button
                      type="button"
                      role="option"
                      aria-selected={idx === activeIndex}
                      className={idx === activeIndex ? "cmd-palette-option selected" : "cmd-palette-option"}
                      key={item.id}
                      onClick={() => item.page && onNavigate(item.page)}
                      onMouseEnter={() => setActiveIndex(idx)}
                    >
                      <span className="cmd-palette-option-icon" aria-hidden="true">›</span>
                      <span className="cmd-palette-option-label">{item.label}</span>
                    </button>
                  );
                })}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
