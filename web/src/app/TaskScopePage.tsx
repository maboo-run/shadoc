import { useEffect, useRef, useState, type FormEvent } from "react";
import type { AppAPI } from "./App";
import { OperationFeedback, useOperation } from "./OperationFeedback";
import { translate, type Locale } from "../i18n";

type ScopeDisposition = "included" | "excluded" | "unreadable" | "attention";
type ScopeEntryType = "file" | "directory" | "symlink" | "special" | "unknown";

type ScopeEntry = {
  ordinal: number;
  path: string;
  type: ScopeEntryType;
  disposition: ScopeDisposition;
  coverage?: "full" | "partial" | "excluded" | "unavailable";
  totalFiles?: number;
  includedFiles?: number;
  excludedFiles?: number;
  excludedItems?: number;
  unreadableItems?: number;
  attentionItems?: number;
  size?: number;
  reasonCode?: string;
  ruleIndexes?: number[];
};

type ScopeImpact = {
  rule: string;
  reason?: string;
  matchedFiles: number;
  estimatedBytes: number;
};

type ScopeSummary = {
  scannedItems?: number;
  totalFiles?: number;
  includedFiles?: number;
  includedBytes?: number;
  excludedFiles?: number;
  excludedBytes?: number;
  unreadableItems?: number;
  attentionItems?: number;
  truncated?: boolean;
  entriesAvailable?: boolean;
  entriesUnavailableReason?: string;
  activeRules?: ScopeImpact[];
  suggestions?: ScopeImpact[];
};

type ScopePreview = {
  previewId: string;
  expiresAt?: string;
  requiresDeleteConfirmation?: boolean;
  summary: ScopeSummary;
};

type ScopeEntryPage = {
  items: ScopeEntry[];
  nextCursor?: string;
  truncated: boolean;
};

type ScopeView = "all" | "included" | "attention" | "excluded" | "rules";

export function TaskScopePage({ taskId, taskName, task, api, locale, onBack, onSaved }: {
  taskId: string;
  taskName: string;
  task?: Record<string, unknown>;
  api: AppAPI;
  locale: Locale;
  onBack(): void;
  onSaved?(task: Record<string, unknown>): void | Promise<void>;
}) {
  const t = (source: string) => translate(locale, source);
  const operation = useOperation(api);
  const started = useRef(false);
  const handledOperation = useRef("");
  const requestSequence = useRef(0);
  const [preview, setPreview] = useState<ScopePreview | null>(null);
  const [view, setView] = useState<ScopeView>("all");
  const [currentDirectory, setCurrentDirectory] = useState("");
  const [searchDraft, setSearchDraft] = useState("");
  const [search, setSearch] = useState("");
  const [entries, setEntries] = useState<ScopeEntry[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [expanded, setExpanded] = useState<number | null>(null);
  const [loadingEntries, setLoadingEntries] = useState(false);
  const [error, setError] = useState("");
  const configuredExclusions = taskExclusions(task);
  const [savedExclusions, setSavedExclusions] = useState(configuredExclusions);
  const [previewedExclusions, setPreviewedExclusions] = useState(configuredExclusions);
  const [draftExclusionText, setDraftExclusionText] = useState(configuredExclusions.join("\n"));
  const [savingRules, setSavingRules] = useState(false);
  const [saveMessage, setSaveMessage] = useState("");
  const [deleteConfirmed, setDeleteConfirmed] = useState(false);
  const draftExclusions = parseExclusionRules(draftExclusionText);

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    void startInventory(false);
    // The operation controller is intentionally adopted once for this task.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [taskId]);

  useEffect(() => {
    const record = operation.operation;
    if (!record || record.status !== "success") return;
    const key = `${record.id}:${record.status}`;
    if (handledOperation.current === key) return;
    handledOperation.current = key;
    const previewId = String(record.detail?.previewId ?? "");
    const summary = record.detail?.summary;
    if (!previewId || !summary || typeof summary !== "object") {
      setError(t("范围清单已完成，但响应中缺少预览结果"));
      return;
    }
    setPreview({
      previewId,
      expiresAt: typeof record.detail?.expiresAt === "string" ? record.detail.expiresAt : undefined,
      requiresDeleteConfirmation: record.detail?.requiresDeleteConfirmation === true,
      summary: summary as ScopeSummary,
    });
    setPreviewedExclusions(Array.isArray(record.detail?.exclusions)
      ? record.detail.exclusions.map((rule) => String(rule))
      : savedExclusions);
    setDeleteConfirmed(false);
    setSaveMessage("");
    setCurrentDirectory("");
    setSearch("");
    setSearchDraft("");
    setError("");
  }, [locale, operation.operation, savedExclusions]);

  useEffect(() => {
    if (!preview?.summary.entriesAvailable || view === "rules") {
      requestSequence.current += 1;
      setEntries([]);
      setNextCursor("");
      setLoadingEntries(false);
      return;
    }
    void loadEntries("", false);
    // Every filter change starts a fresh, server-filtered page.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [preview?.previewId, preview?.summary.entriesAvailable, view, currentDirectory, search]);

  async function loadEntries(cursor: string, append: boolean) {
    if (!preview) return;
    const requestID = ++requestSequence.current;
    setLoadingEntries(true);
    setError("");
    const query = new URLSearchParams();
    query.set("view", view === "rules" ? "all" : view);
    query.set("limit", "200");
    query.set("children", "true");
    query.set("parent", currentDirectory);
    if (search) query.set("q", search);
    if (cursor) query.set("cursor", cursor);
    try {
      const value = await api.action(`/api/task-scope-previews/${encodeURIComponent(preview.previewId)}/entries?${query.toString()}`) as ScopeEntryPage;
      if (requestSequence.current !== requestID) return;
      const items = Array.isArray(value.items) ? value.items : [];
      setEntries((current) => append ? [...current, ...items] : items);
      setNextCursor(typeof value.nextCursor === "string" ? value.nextCursor : "");
      setExpanded(null);
    } catch (cause) {
      if (requestSequence.current === requestID) {
        setEntries([]);
        setNextCursor("");
        setError(cause instanceof Error ? cause.message : t("无法读取文件清单"));
      }
    } finally {
      if (requestSequence.current === requestID) setLoadingEntries(false);
    }
  }

  function applySearch(event: FormEvent) {
    event.preventDefault();
    setSearch(searchDraft.trim());
  }

  function refresh() {
    void startInventory(!sameRules(savedExclusions, draftExclusions));
  }

  async function startInventory(useDraft: boolean) {
    requestSequence.current += 1;
    setPreview(null);
    setEntries([]);
    setNextCursor("");
    setExpanded(null);
    setCurrentDirectory("");
    setSearch("");
    setSearchDraft("");
    setError("");
    setSaveMessage("");
    await operation.start(
      `/api/tasks/${encodeURIComponent(taskId)}/scope-inventory`,
      useDraft ? { exclusions: draftExclusions } : {},
    );
  }

  async function saveRules() {
    if (!preview || !sameRules(previewedExclusions, draftExclusions)) return;
    setSavingRules(true);
    setError("");
    setSaveMessage("");
    try {
      const saved = await api.action(`/api/tasks/${encodeURIComponent(taskId)}/scope-rules`, {
        previewId: preview.previewId,
        exclusions: draftExclusions,
        rsyncDeleteConfirmed: deleteConfirmed,
      }) as Record<string, unknown>;
      setSavedExclusions(draftExclusions);
      setPreviewedExclusions(draftExclusions);
      setSaveMessage(t("规则已保存，当前预览与任务配置一致。"));
      void Promise.resolve(onSaved?.(saved)).catch(() => undefined);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法保存排除规则"));
    } finally {
      setSavingRules(false);
    }
  }

  const summary = preview?.summary;
  const unavailable = preview && !summary?.entriesAvailable;
  const pendingCount = symmetricDifferenceCount(savedExclusions, draftExclusions);
  const previewMatchesDraft = Boolean(preview) && sameRules(previewedExclusions, draftExclusions);
  const crumbs = directoryCrumbs(currentDirectory);
  const parentDirectory = directoryParent(currentDirectory);
  const views: Array<[ScopeView, string]> = [
    ["all", "全部"],
    ["included", "将保护"],
    ["attention", "需关注"],
    ["excluded", "已排除"],
    ["rules", "排除规则"],
  ];

  return <div className="task-scope-page">
    <header className="page-header task-scope-header">
      <div>
        <button className="back-button" type="button" aria-label={t("返回任务编辑")} onClick={onBack}>← {t("返回任务编辑")}</button>
        <span className="task-scope-eyebrow">{taskName || taskId}</span>
        <h1>{t("保护范围透镜")}</h1>
        <p>{t("先查看源目录中实际发现的文件，再核对哪些会被保护、排除或因权限无法读取。")}</p>
      </div>
      <button className="secondary-button" type="button" disabled={operation.active} onClick={refresh}>{t(operation.active ? "正在扫描…" : "重新扫描")}</button>
    </header>

    <OperationFeedback operation={operation} locale={locale} compact persistTerminal cancellable />
    {error && <p className="error-message" role="alert">{error}</p>}

    {summary && <section className="scope-lens-summary" aria-label={t("范围统计")}>
      <dl className="scope-lens-summary-grid">
        <ScopeStat label={t("源目录总文件")} value={summary.totalFiles} locale={locale} tone="neutral" />
        <ScopeStat label={t("将保护")} value={summary.includedFiles} locale={locale} tone="included" />
        <ScopeStat label={t("已排除")} value={summary.excludedFiles} locale={locale} tone="excluded" />
        <ScopeStat label={t("需关注")} value={(summary.unreadableItems ?? 0) + (summary.attentionItems ?? 0)} locale={locale} tone="attention" />
      </dl>
      {summary.truncated && <p className="scope-lens-truncated" role="alert">{t("扫描达到 100,000 项上限，当前统计和清单并不完整。")}</p>}
    </section>}

    {unavailable && <section className="content-section scope-lens-unavailable" role="status">
      <h2>{t("逐个文件清单暂不可用")}</h2>
      <p>{t(summary?.entriesUnavailableReason === "agent_inventory_upgrade_required"
        ? "当前 Agent 只能返回范围统计，升级 Agent 后才能查看逐个文件。"
        : "当前执行节点只能返回范围统计，暂时无法查看逐个文件。")}</p>
    </section>}

    {summary?.entriesAvailable && <section className="content-section scope-lens-browser">
      <div className="scope-lens-tabs" role="tablist" aria-label={t("范围视图")}>
        {views.map(([value, label]) => <button
          key={value}
          role="tab"
          type="button"
          aria-selected={view === value}
          className={view === value ? "selected" : ""}
          onClick={() => setView(value)}
        >{t(label)}</button>)}
      </div>

      {pendingCount > 0 && <div className={`scope-exclusion-draft${previewMatchesDraft ? " preview-ready" : ""}`} role="status">
        <div>
          <strong>{previewMatchesDraft
            ? t("当前结果是临时预览，尚未保存到任务。")
            : locale === "en-US" ? `${pendingCount} rule changes have not been previewed` : `${pendingCount} 项规则变更尚未预览`}</strong>
          <p>{t(previewMatchesDraft
            ? "离开页面或预览过期后，未保存的规则不会生效。"
            : "先用当前草稿重新扫描；确认结果后才能保存规则。")}</p>
          <p>{t("恢复已排除项目会移除命中的排除规则，同一规则影响的其他内容也会恢复保护。")}</p>
          {previewMatchesDraft && preview?.requiresDeleteConfirmation && <label className="scope-delete-confirmation">
            <input type="checkbox" checked={deleteConfirmed} onChange={(event) => setDeleteConfirmed(event.target.checked)} />
            {t("我确认 rsync 删除预览中的目标变化")}
          </label>}
        </div>
        <span className="scope-exclusion-actions">
          <button className="text-button" type="button" disabled={operation.active || savingRules} onClick={() => {
            setDraftExclusionText(savedExclusions.join("\n"));
            setSaveMessage("");
          }}>{t("撤销全部")}</button>
          {previewMatchesDraft
            ? <button className="primary-button" type="button" disabled={savingRules || Boolean(preview?.requiresDeleteConfirmation && !deleteConfirmed)} onClick={() => void saveRules()}>{t(savingRules ? "正在保存…" : "保存规则")}</button>
            : <button className="primary-button" type="button" disabled={operation.active} onClick={() => void startInventory(true)}>{t(operation.active ? "正在扫描…" : "预览规则效果")}</button>}
        </span>
      </div>}
      {saveMessage && <div className="scope-rules-saved" role="status">{saveMessage}</div>}

      {view === "rules" ? <ExclusionRuleEditor
        summary={summary}
        locale={locale}
        configuredRules={savedExclusions}
        previewedRules={previewedExclusions}
        draftRules={draftExclusions}
        draftText={draftExclusionText}
        editable
        onDraftTextChange={setDraftExclusionText}
      /> : <>
        <form className="scope-lens-filters" onSubmit={applySearch}>
          <label>{t("筛选当前层级")}
            <input value={searchDraft} maxLength={256} placeholder={t("输入当前文件夹中的名称")} onChange={(event) => setSearchDraft(event.target.value)} />
          </label>
          <button className="secondary-button" type="submit" disabled={loadingEntries}>{t("搜索")}</button>
        </form>
        <div className="scope-directory-navigation">
          {currentDirectory && <button className="scope-directory-up" type="button" onClick={() => openDirectory(parentDirectory)}>
            <span aria-hidden="true">←</span> {t("返回上一层")}
          </button>}
          <nav className="scope-directory-crumbs" aria-label={t("目录位置")}>
            <button type="button" aria-current={currentDirectory === "" ? "location" : undefined} onClick={() => openDirectory("")}>{t("源目录")}</button>
            {crumbs.map((crumb) => <span key={crumb.path}>
              <span aria-hidden="true">/</span>
              <button type="button" aria-current={currentDirectory === crumb.path ? "location" : undefined} onClick={() => openDirectory(crumb.path)}>{crumb.name}</button>
            </span>)}
          </nav>
        </div>
        <p className="scope-lens-path-note">{t("每层只显示当前文件夹的直接内容；打开文件夹后再查看其中的文件。")}</p>
        <ul className="scope-entry-list" aria-label={t("源文件清单")}>
          {entries.map((entry) => {
            const open = expanded === entry.ordinal;
            const name = scopeEntryName(entry.path);
            const matchedRules = matchedEntryRules(entry, previewedExclusions);
            const pendingExclude = !savedExclusions.includes(entry.path) && draftExclusions.includes(entry.path);
            const pendingRestore = matchedRules.some((rule) => !draftExclusions.includes(rule));
            const canRestore = entry.disposition === "excluded" || pendingRestore;
            return <li key={`${entry.ordinal}:${entry.path}`} className={`scope-entry scope-entry-${entry.disposition} scope-entry-coverage-${entry.coverage ?? "item"}`}>
              <span className="scope-entry-rail" aria-hidden="true" />
              <span className="scope-entry-icon" aria-hidden="true">{entryIcon(entry.type)}</span>
              <span className="scope-entry-main">
                <strong>{name}</strong>
                <small>{entry.type === "directory"
                  ? directoryCoverageText(entry, locale)
                  : `${entryDisposition(entry.disposition, locale)} · ${entryTypeLabel(entry.type, locale)}`}</small>
                {(pendingExclude || pendingRestore) && <span className="scope-entry-pending">{t(pendingExclude ? "排除未保存" : "恢复未保存")}</span>}
                {open && <span className="scope-entry-detail">
                  <span>{t("估算大小")}：{formatScopeBytes(entry.size, locale)}</span>
                  <span>{t("判定原因")}：{entryReason(entry.reasonCode, locale)}</span>
                  {!!entry.ruleIndexes?.length && <span>{t("命中规则序号")}：{entry.ruleIndexes.map((index) => index + 1).join(", ")}</span>}
                </span>}
              </span>
              <span className="scope-entry-actions">
                {entry.type === "directory"
                  ? <button className="text-button" type="button" aria-label={`${t("打开文件夹")} ${name}`} onClick={() => openDirectory(entry.path)}>{t("打开")}</button>
                  : <button className="text-button" type="button" aria-expanded={open} aria-label={`${t("查看")} ${entry.path} ${t("详情")}`} onClick={() => setExpanded(open ? null : entry.ordinal)}>{t(open ? "收起" : "详情")}</button>}
                <button className="text-button scope-exclusion-toggle" type="button" onClick={() => toggleExclusion(entry, matchedRules, pendingExclude, pendingRestore)}>
                  {t(pendingExclude ? "撤销排除" : pendingRestore ? "撤销恢复" : canRestore ? "恢复保护" : entry.type === "directory" ? "排除文件夹" : "排除此文件")}
                </button>
              </span>
            </li>;
          })}
          {!loadingEntries && entries.length === 0 && <li className="scope-entry-empty">{t("当前筛选下没有文件")}</li>}
        </ul>
        <div className="scope-lens-load-more" role="status" aria-live="polite">
          {loadingEntries && <span>{t("正在读取文件清单…")}</span>}
          {nextCursor && <button className="secondary-button" type="button" disabled={loadingEntries} onClick={() => void loadEntries(nextCursor, true)}>{t("加载更多")}</button>}
        </div>
      </>}
    </section>}
  </div>;

  function openDirectory(path: string) {
    setCurrentDirectory(path);
    setSearch("");
    setSearchDraft("");
    setExpanded(null);
  }

  function toggleExclusion(entry: ScopeEntry, matchedRules: string[], pendingExclude: boolean, pendingRestore: boolean) {
    if (pendingExclude) {
      updateDraftExclusions((current) => current.filter((rule) => rule !== entry.path));
      return;
    }
    if (pendingRestore) {
      updateDraftExclusions((current) => [...current, ...matchedRules.filter((rule) => !current.includes(rule))]);
      return;
    }
    if (entry.disposition === "excluded") {
      const remove = matchedRules.length ? matchedRules : [entry.path];
      updateDraftExclusions((current) => current.filter((rule) => !remove.includes(rule)));
      return;
    }
    updateDraftExclusions((current) => current.includes(entry.path) ? current : [...current, entry.path]);
  }

  function updateDraftExclusions(update: (current: string[]) => string[]) {
    setDraftExclusionText((current) => update(parseExclusionRules(current)).join("\n"));
  }
}

function taskExclusions(task: Record<string, unknown> | undefined): string[] {
  const source = (task?.engine === "rsync" || task?.kind === "rsync" ? task.rsync : task?.directory) as Record<string, unknown> | undefined;
  return Array.isArray(source?.exclusions)
    ? source.exclusions.map((rule) => String(rule).trim()).filter(Boolean)
    : [];
}

function parseExclusionRules(value: string): string[] {
  return Array.from(new Set(value.split("\n").map((rule) => rule.trim()).filter(Boolean)));
}

function symmetricDifferenceCount(left: string[], right: string[]) {
  const first = new Set(left);
  const second = new Set(right);
  let count = 0;
  first.forEach((value) => { if (!second.has(value)) count += 1; });
  second.forEach((value) => { if (!first.has(value)) count += 1; });
  return count;
}

function sameRules(left: string[], right: string[]) {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function matchedEntryRules(entry: ScopeEntry, exclusions: string[]) {
  return (entry.ruleIndexes ?? [])
    .map((index) => exclusions[index])
    .filter((rule): rule is string => typeof rule === "string");
}

function scopeEntryName(path: string) {
  const segments = path.split("/");
  return segments[segments.length - 1] || path;
}

function directoryCrumbs(path: string) {
  const segments = path.split("/").filter(Boolean);
  return segments.map((name, index) => ({ name, path: segments.slice(0, index + 1).join("/") }));
}

function directoryParent(path: string) {
  const segments = path.split("/").filter(Boolean);
  segments.pop();
  return segments.join("/");
}

function directoryCoverageText(entry: ScopeEntry, locale: Locale) {
  const total = new Intl.NumberFormat(locale).format(Number(entry.totalFiles ?? 0));
  const included = new Intl.NumberFormat(locale).format(Number(entry.includedFiles ?? 0));
  if (locale === "en-US") {
    switch (entry.coverage) {
      case "partial": return `Partially protected · ${included} / ${total} files protected`;
      case "excluded": return `Excluded · ${total} files`;
      case "unavailable": return "Unavailable · contents could not be read";
      default: return `Fully protected · all ${total} files`;
    }
  }
  switch (entry.coverage) {
    case "partial": return `部分保护 · 将保护 ${included} / ${total} 个文件`;
    case "excluded": return `已排除 · 文件夹下 ${total} 个文件`;
    case "unavailable": return "无法保护 · 文件夹内容无法读取";
    default: return `完整保护 · 文件夹下全部 ${total} 个文件`;
  }
}

function ScopeStat({ label, value, locale, tone }: { label: string; value?: number; locale: Locale; tone: string }) {
  return <div className={`scope-lens-stat scope-lens-stat-${tone}`}><dt>{label}</dt><dd>{value == null ? "—" : new Intl.NumberFormat(locale).format(value)}</dd></div>;
}

function ExclusionRuleEditor({ summary, locale, configuredRules, previewedRules, draftRules, draftText, editable, onDraftTextChange }: {
  summary: ScopeSummary;
  locale: Locale;
  configuredRules: string[];
  previewedRules: string[];
  draftRules: string[];
  draftText: string;
  editable: boolean;
  onDraftTextChange(value: string): void;
}) {
  const t = (source: string) => translate(locale, source);
  const impacts = Array.isArray(summary.activeRules) ? summary.activeRules : [];
  const suggestions = Array.isArray(summary.suggestions) ? summary.suggestions : [];
  const impactByRule = new Map(impacts.map((impact) => [impact.rule, impact]));
  const previewRules = Array.from(new Set([...previewedRules, ...impacts.map((impact) => impact.rule), ...draftRules]));

  const updateSuggestion = (rule: string, selected: boolean) => {
    const next = selected
      ? [...draftRules, rule]
      : draftRules.filter((item) => item !== rule);
    onDraftTextChange(Array.from(new Set(next)).join("\n"));
  };

  return <div className="scope-rule-editor">
    <section className="scope-rule-compose" aria-labelledby="scope-rule-compose-title">
      <div>
        <h2 id="scope-rule-compose-title">{t("编写排除规则")}</h2>
        <p>{t("每行一条规则；也可以在文件列表中直接排除或恢复文件。")}</p>
      </div>
      <label>{t("排除规则（每行一条）")}
        <textarea
          value={draftText}
          readOnly={!editable}
          placeholder={t("默认不排除任何内容；每条规则都需要通过预览确认影响。")}
          onChange={(event) => onDraftTextChange(event.target.value)}
        />
      </label>
    </section>

    <section className="scope-rule-preview" aria-labelledby="scope-rule-preview-title">
      <div>
        <h2 id="scope-rule-preview-title">{t("当前预览中的规则影响")}</h2>
        <p>{t("这里显示最近一次扫描的结果；继续修改草稿后需要重新预览。")}</p>
      </div>
      {previewRules.length ? <ol>{previewRules.map((rule) => {
        const impact = impactByRule.get(rule);
        const removed = configuredRules.includes(rule) && !draftRules.includes(rule);
        const added = !configuredRules.includes(rule) && draftRules.includes(rule);
        return <li key={rule} className={removed || added ? "pending" : ""}>
          <code>{rule}</code>
          <span>{impact ? impactText(impact, locale) : t("重新扫描后显示影响")}</span>
          {(removed || added) && <em>{t(removed ? "待移除" : "待新增")}</em>}
        </li>;
      })}</ol> : <div className="scope-rule-empty">
        <h3>{t("没有生效的排除规则")}</h3>
        <p>{t("当前源目录中的可读文件默认都会纳入保护。")}</p>
      </div>}
    </section>

    {!!suggestions.length && <section className="scope-rule-suggestions" aria-labelledby="scope-rule-suggestions-title">
      <div>
        <h2 id="scope-rule-suggestions-title">{t("排除建议")}</h2>
        <p>{t("建议不会自动应用；选中后会加入规则草稿。")}</p>
      </div>
      <div className="scope-rule-suggestion-list">{suggestions.map((suggestion) => {
        const selected = draftRules.includes(suggestion.rule);
        return <label key={suggestion.rule}>
          <input
            type="checkbox"
            aria-label={`${t("采用建议")} ${suggestion.rule}`}
            checked={selected}
            disabled={!editable}
            onChange={(event) => updateSuggestion(suggestion.rule, event.target.checked)}
          />
          <span><code>{suggestion.rule}</code><small>{t(String(suggestion.reason ?? ""))}</small></span>
          <em>{impactText(suggestion, locale)}</em>
        </label>;
      })}</div>
    </section>}
  </div>;
}

function impactText(impact: ScopeImpact, locale: Locale) {
  const files = new Intl.NumberFormat(locale).format(Number(impact.matchedFiles ?? 0));
  const bytes = formatScopeBytes(impact.estimatedBytes, locale);
  return locale === "en-US" ? `${files} files, ${bytes}` : `${files} 个文件，${bytes}`;
}

function entryIcon(type: ScopeEntryType) {
  switch (type) {
    case "directory": return "▱";
    case "symlink": return "↗";
    case "special": return "◇";
    default: return "·";
  }
}

function entryDisposition(value: ScopeDisposition, locale: Locale) {
  return translate(locale, ({ included: "将保护", excluded: "已排除", unreadable: "无法读取", attention: "需关注" } as Record<string, string>)[value] ?? "需关注");
}

function entryTypeLabel(value: ScopeEntryType, locale: Locale) {
  return translate(locale, ({ file: "普通文件", directory: "目录", symlink: "符号链接", special: "特殊文件", unknown: "未知类型" } as Record<string, string>)[value] ?? "未知类型");
}

function entryReason(value: string | undefined, locale: Locale) {
  return translate(locale, ({
    exclusion_rule: "命中排除规则",
    permission_denied: "当前服务账号没有读取权限",
    unreadable: "读取失败",
    metadata_unavailable: "无法读取文件元数据",
    special_file: "特殊文件不会作为普通文件处理",
  } as Record<string, string>)[String(value)] ?? "无额外说明");
}

function formatScopeBytes(raw: number | undefined, locale: Locale) {
  const bytes = Math.max(0, Number(raw ?? 0));
  if (bytes < 1024) return `${new Intl.NumberFormat(locale).format(bytes)} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${new Intl.NumberFormat(locale, { minimumFractionDigits: value < 10 ? 1 : 0, maximumFractionDigits: 1 }).format(value)} ${units[unit]}`;
}
