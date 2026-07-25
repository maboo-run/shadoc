import { type ReactNode, type RefObject } from "react";
import { translate, type Locale } from "../i18n";
import type { AppAPI } from "./App";
import { ApplicationVersionStatus } from "./ApplicationVersionStatus";

export const sidebarNavigation = [
  "仪表盘", "连接管理", "备份仓库", "备份任务",
  "快照与恢复", "活动与记录", "系统",
];

export const navigationGroups = {
  "连接管理": ["远程主机", "Agent 节点", "数据库实例"],
  "活动与记录": ["运行记录", "告警历史", "投递记录", "审计日志"],
  "系统": ["兼容性中心", "通知配置", "Agent 服务", "安全设置", "配置备份与恢复", "数据生命周期", "界面语言"],
} as const;

export const navigation = [
  "仪表盘",
  "兼容性中心",
  "远程主机",
  "Agent 节点",
  "备份仓库",
  "数据库实例",
  "备份任务",
  "快照与恢复",
  "运行记录",
  "告警历史",
  "投递记录",
  "审计日志",
  "通知配置",
  "Agent 服务",
  "安全设置",
  "配置备份与恢复",
  "数据生命周期",
  "界面语言",
];

export function NavigationIcon({ item }: { item: string }) {
  const paths: Record<string, ReactNode> = {
    "仪表盘": <path d="M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h6v6h-6z" />,
    "连接管理": <><circle cx="6" cy="12" r="3" /><circle cx="18" cy="6" r="3" /><circle cx="18" cy="18" r="3" /><path d="m9 11 6-4M9 13l6 4" /></>,
    "远程主机": <><rect x="3" y="5" width="18" height="12" rx="2" /><path d="M8 21h8M12 17v4M7 9h.01M10 9h.01" /></>,
    "Agent 节点": <><circle cx="12" cy="12" r="3" /><circle cx="5" cy="6" r="2" /><circle cx="19" cy="6" r="2" /><circle cx="5" cy="18" r="2" /><circle cx="19" cy="18" r="2" /><path d="m7 7.5 2.5 2M17 7.5l-2.5 2M7 16.5l2.5-2M17 16.5l-2.5-2" /></>,
    "备份仓库": <><ellipse cx="12" cy="5" rx="8" ry="3" /><path d="M4 5v7c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 12v7c0 1.7 3.6 3 8 3s8-1.3 8-3v-7" /></>,
    "数据库实例": <><ellipse cx="12" cy="5" rx="7" ry="3" /><path d="M5 5v7c0 1.7 3.1 3 7 3s7-1.3 7-3V5M5 12v7c0 1.7 3.1 3 7 3s7-1.3 7-3v-7" /></>,
    "备份任务": <><path d="M6 3h9l4 4v14H6z" /><path d="M14 3v5h5M9 13h6M9 17h6" /></>,
    "备份计划": <><rect x="3" y="5" width="18" height="16" rx="2" /><path d="M16 3v4M8 3v4M3 10h18M8 14h.01M12 14h.01M16 14h.01M8 18h.01M12 18h.01" /></>,
    "快照与恢复": <><path d="M4 7v5h5M20 17v-5h-5" /><path d="M6.1 17A8 8 0 0 0 20 12M17.9 7A8 8 0 0 0 4 12" /></>,
    "活动与记录": <><path d="M4 19V9M10 19V5M16 19v-7M22 19H2" /></>,
    "系统": <><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1a1.7 1.7 0 0 0 1.9.3A1.7 1.7 0 0 0 10 3v-.2h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z" /></>,
  };
  return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">{paths[item]}</svg>;
}

type SidebarProps = {
  api: AppAPI;
  locale: Locale;
  username: string;
  activePage: string;
  mobile?: boolean;
  mobileNavigationOpen: boolean;
  firstNavigationItem?: RefObject<HTMLButtonElement | null>;
  onNavigate(page: string): void;
  onCloseMobile(): void;
  onLogout(): void;
};

export function Sidebar({ api, locale, username, activePage, mobile = false, mobileNavigationOpen, firstNavigationItem, onNavigate, onCloseMobile, onLogout }: SidebarProps) {
  const t = (source: string) => translate(locale, source);
  return (
    <aside
      id="administration-navigation"
      className={`sidebar${mobileNavigationOpen ? " mobile-open" : ""}`}
      aria-hidden={mobile && !mobileNavigationOpen}
      inert={mobile && !mobileNavigationOpen ? true : undefined}
    >
      <div className="brand">
        <span className="brand-mark"><img src="/shadoc-icon.png" alt="" /></span>
        <span>影刻 <small>Shadoc</small></span>
      </div>
      <nav aria-label={t("主导航")}>
        {sidebarNavigation.map((item, index) => {
          const children = navigationGroups[item as keyof typeof navigationGroups];
          const selected = item === activePage || !!children?.includes(activePage as never);
          const target = children?.[0] ?? item;
          return (
            <button
              ref={index === 0 ? firstNavigationItem : undefined}
              className={selected ? "nav-item selected" : "nav-item"}
              key={item}
              type="button"
              onClick={() => { onCloseMobile(); onNavigate(target); }}
            >
              <span className="nav-icon" aria-hidden="true"><NavigationIcon item={item} /></span>
              {t(item)}
            </button>
          );
        })}
      </nav>
      <div className="sidebar-account">
        <div className="sidebar-user">
          <span className="status-dot" />
          {username}
          <button className="text-button" type="button" onClick={onLogout}>{t("退出登录")}</button>
        </div>
        <ApplicationVersionStatus api={api} locale={locale} />
      </div>
    </aside>
  );
}
