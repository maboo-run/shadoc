import { useEffect, useRef, useState } from "react";
import { useModalFocus } from "./useModalFocus";
import { ModalPortal } from "./ModalPortal";
import { translate, type Locale } from "../i18n";

export type PlanSchedule =
  | { kind: "daily"; timeOfDay: string }
  | { kind: "weekly"; dayOfWeek: number; timeOfDay: string }
  | { kind: "interval"; intervalHours: number };

export type PlanValue = {
  name: string;
  schedule: PlanSchedule;
  timezone: string;
  maxParallel: number;
  catchUpWindowMinutes: number;
  taskIds: string[];
  enabled: boolean;
};

export type PlanRecord = Omit<PlanValue, "catchUpWindowMinutes"> & {
  catchUpWindowMinutes?: number;
  id?: string;
};

type PlanEditorAPI = {
  listResource(resource: string): Promise<Array<Record<string, unknown>>>;
};

type TaskOption = { id: string; name: string; enabled: boolean };

export function PlanEditor({
  api,
  initial,
  onClose,
  onSubmit,
  locale = "zh-CN",
}: {
  api: PlanEditorAPI;
  initial: PlanRecord | null;
  onClose(): void;
  onSubmit(value: PlanValue): Promise<void>;
  locale?: Locale;
}) {
  const t = (source: string) => translate(locale, source);
  const dialogRef = useRef<HTMLFormElement>(null);
  useModalFocus(dialogRef, onClose);
  const initialSchedule = initial?.schedule;
  const [name, setName] = useState(initial?.name ?? "");
  const [timezone, setTimezone] = useState(initial?.timezone ?? "Asia/Shanghai");
  const [maxParallel, setMaxParallel] = useState(initial?.maxParallel ?? 1);
  const [catchUpWindowMinutes, setCatchUpWindowMinutes] = useState(
    initial?.catchUpWindowMinutes ?? 60,
  );
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [kind, setKind] = useState<PlanSchedule["kind"]>(
    initialSchedule?.kind ?? "daily",
  );
  const [timeOfDay, setTimeOfDay] = useState(
    initialSchedule?.kind === "daily" || initialSchedule?.kind === "weekly"
      ? initialSchedule.timeOfDay
      : "02:30",
  );
  const [dayOfWeek, setDayOfWeek] = useState(
    initialSchedule?.kind === "weekly" ? initialSchedule.dayOfWeek : 1,
  );
  const [intervalHours, setIntervalHours] = useState(
    initialSchedule?.kind === "interval" ? initialSchedule.intervalHours : 6,
  );
  const [taskIds, setTaskIds] = useState<string[]>(initial?.taskIds ?? []);
  const [tasks, setTasks] = useState<TaskOption[]>([]);
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    void api
      .listResource("tasks")
      .then((items) => {
        if (!active) return;
        setTasks(
          items
            .filter(
              (item) => typeof item.id === "string" && typeof item.name === "string",
            )
            .map((item) => ({
              id: String(item.id),
              name: String(item.name),
              enabled: item.enabled === true,
            })),
        );
      })
      .catch(() => {
        if (active) setError("无法读取备份任务");
      });
    return () => {
      active = false;
    };
  }, [api]);

  function schedule(): PlanSchedule {
    if (kind === "weekly") return { kind, dayOfWeek, timeOfDay };
    if (kind === "interval") return { kind, intervalHours };
    return { kind, timeOfDay };
  }

  return (
    <ModalPortal>
      <form
        ref={dialogRef}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="plan-dialog-title"
        onSubmit={(event) => {
          event.preventDefault();
          if (taskIds.length === 0) {
            setError("至少选择一个备份任务");
            return;
          }
          setError("");
          void onSubmit({
            name,
            schedule: schedule(),
            timezone,
            maxParallel,
            catchUpWindowMinutes,
            taskIds,
            enabled,
          }).catch((cause) =>
            setError(cause instanceof Error ? cause.message : "保存失败"),
          );
        }}
      >
        <header>
          <div>
            <h2 id="plan-dialog-title">{t(initial ? "编辑备份计划" : "新建备份计划")}</h2>
            <p>{t("一个计划可在同一时间点触发多个独立任务。")}</p>
          </div>
          <button className="icon-button" type="button" aria-label={t("关闭")} onClick={onClose}>
            ×
          </button>
        </header>
        {error && <p className="form-error">{error}</p>}
        <div className="form-grid">
          <label>
            {t("计划名称")}
            <input value={name} onChange={(event) => setName(event.target.value)} required />
          </label>
          <label>
            {t("时区")}
            <input
              value={timezone}
              onChange={(event) => setTimezone(event.target.value)}
              required
            />
          </label>
          <label>
            {t("计划类型")}
            <select
              value={kind}
              onChange={(event) => setKind(event.target.value as PlanSchedule["kind"])}
            >
              <option value="daily">{t("每日")}</option>
              <option value="weekly">{t("每周")}</option>
              <option value="interval">{t("固定间隔")}</option>
            </select>
          </label>
          {(kind === "daily" || kind === "weekly") && (
            <label>
              {t("执行时间")}
              <input
                type="time"
                value={timeOfDay}
                onChange={(event) => setTimeOfDay(event.target.value)}
                required
              />
            </label>
          )}
          {kind === "weekly" && (
            <label>
              {t("星期")}
              <select
                value={dayOfWeek}
                onChange={(event) => setDayOfWeek(Number(event.target.value))}
              >
                {["星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"].map((day, index) => <option value={index} key={day}>{t(day)}</option>)}
              </select>
            </label>
          )}
          {kind === "interval" && (
            <label>
              {t("间隔小时数")}
              <input
                type="number"
                min="1"
                max="8760"
                value={intervalHours}
                onChange={(event) => setIntervalHours(Number(event.target.value))}
                required
              />
            </label>
          )}
          <label>
            {t("同时执行数")}
            <input
              type="number"
              min="1"
              value={maxParallel}
              onChange={(event) => setMaxParallel(Number(event.target.value))}
              required
            />
          </label>
          <label>
            {t("离线补跑宽限（分钟）")}
            <input
              aria-label={t("离线补跑宽限（分钟）")}
              type="number"
              min="0"
              max="10080"
              value={catchUpWindowMinutes}
              onChange={(event) => setCatchUpWindowMinutes(Number(event.target.value))}
              required
            />
            <span className="field-hint">
              {t("只补跑宽限期内最近一次；更早发生会记录为错过。")}
            </span>
          </label>
          <label>
            <input
              type="checkbox"
              checked={enabled}
              onChange={(event) => {
                const next = event.target.checked;
                setEnabled(next);
                if (next) {
                  const enabledIDs = new Set(
                    tasks.filter((task) => task.enabled).map((task) => task.id),
                  );
                  setTaskIds((current) =>
                    current.filter((id) => enabledIDs.has(id)),
                  );
                }
              }}
            />{" "}
            {t("启用计划")}
          </label>
          <fieldset className="full-field">
            <legend>{t("备份任务")}</legend>
            {tasks.map((task) => {
              const label = task.enabled ? task.name : `${task.name}${t("（已停用）")}`;
              return (
                <label key={task.id}>
                  <input
                    type="checkbox"
                    checked={taskIds.includes(task.id)}
                    disabled={enabled && !task.enabled}
                    onChange={(event) =>
                      setTaskIds((current) =>
                        event.target.checked
                          ? [...current, task.id]
                          : current.filter((id) => id !== task.id),
                      )
                    }
                  />{" "}
                  {label}
                </label>
              );
            })}
            {!tasks.length && <p>{t("尚无可选备份任务")}</p>}
          </fieldset>
        </div>
        <footer>
          <button className="secondary-button" type="button" onClick={onClose}>
            {t("取消")}
          </button>
          <button className="primary-button" type="submit">
            {t("保存计划")}
          </button>
        </footer>
      </form>
    </ModalPortal>
  );
}
