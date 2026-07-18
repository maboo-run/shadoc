import { useEffect, useState, type FormEvent } from "react";
import { translate, type Locale } from "../i18n";
import type { AppAPI } from "./App";
import { Toast } from "./Toast";

type NtfyState = { baseUrl: string; topic: string; hasToken: boolean; enabled: boolean };
type WebhookState = { endpoint: string; authMode: "none" | "bearer" | "hmac-sha256"; hasSecret: boolean; enabled: boolean };
type LoadState = { ntfy: boolean; webhook: boolean };
type Channel = keyof LoadState;

const initialNtfy: NtfyState = { baseUrl: "https://ntfy.sh", topic: "", hasToken: false, enabled: false };
const initialWebhook: WebhookState = { endpoint: "", authMode: "none", hasSecret: false, enabled: false };

export function NotificationChannels({ api, locale }: { api: AppAPI; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const [message, setMessage] = useState("");
  const [ntfy, setNtfy] = useState<NtfyState>(initialNtfy);
  const [webhook, setWebhook] = useState<WebhookState>(initialWebhook);
  const [clearToken, setClearToken] = useState(false);
  const [clearWebhookSecret, setClearWebhookSecret] = useState(false);
  const [loaded, setLoaded] = useState<LoadState>({ ntfy: false, webhook: false });
  const [errors, setErrors] = useState<Partial<Record<Channel, string>>>({});
  const [testing, setTesting] = useState<Channel | "">("");

  const applyNtfy = (value: unknown) => {
    const saved = value as Record<string, unknown>;
    setNtfy({ baseUrl: String(saved.baseUrl ?? initialNtfy.baseUrl), topic: String(saved.topic ?? ""), hasToken: Boolean(saved.hasToken), enabled: saved.enabled === true });
  };
  const applyWebhook = (value: unknown) => {
    const saved = value as Record<string, unknown>;
    const authMode = ["bearer", "hmac-sha256"].includes(String(saved.authMode)) ? String(saved.authMode) as WebhookState["authMode"] : "none";
    setWebhook({ endpoint: String(saved.endpoint ?? ""), authMode, hasSecret: Boolean(saved.hasSecret), enabled: saved.enabled === true });
  };
  useEffect(() => {
    let active = true;
    const load = async (channel: Channel, path: string, apply: (value: unknown) => void, error: string) => {
      try {
        const value = await api.action(path);
        if (!active) return;
        apply(value);
        setLoaded((current) => ({ ...current, [channel]: true }));
      } catch {
        if (active) setErrors((current) => ({ ...current, [channel]: error }));
      }
    };
    void Promise.all([
      load("ntfy", "/api/ntfy", applyNtfy, t("无法读取 ntfy 配置，已禁止保存以避免覆盖现有设置")),
      load("webhook", "/api/webhook", applyWebhook, t("无法读取 Webhook 配置，已禁止保存以避免覆盖现有设置")),
    ]);
    return () => { active = false; };
    // Translation changes remount this page through the parent route.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api]);

  const testChannel = (channel: Channel, path: string) => {
    setTesting(channel);
    void api.action(path, {})
      .then(() => setMessage(t("测试通知已送达")))
      .catch((reason) => setMessage(reason instanceof Error ? reason.message : t("测试通知发送失败，请检查通道配置")))
      .finally(() => setTesting(""));
  };

  const saveNtfy = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    void api.action("/api/ntfy", { baseUrl: ntfy.baseUrl, topic: ntfy.topic, token: String(form.get("token") ?? ""), clearToken, enabled: ntfy.enabled })
      .then(() => api.action("/api/ntfy"))
      .then((value) => { applyNtfy(value); setClearToken(false); setMessage(t("ntfy 配置已保存并已从服务端确认")); })
      .catch((reason) => setMessage(reason instanceof Error ? reason.message : t("ntfy 配置保存失败")));
  };

  const saveWebhook = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    void api.action("/api/webhook", { endpoint: webhook.endpoint, authMode: webhook.authMode, secret: String(form.get("webhookSecret") ?? ""), clearSecret: clearWebhookSecret, enabled: webhook.enabled })
      .then(() => api.action("/api/webhook"))
      .then((value) => { applyWebhook(value); setClearWebhookSecret(false); setMessage(t("Webhook 配置已保存并已从服务端确认")); })
      .catch((reason) => setMessage(reason instanceof Error ? reason.message : t("Webhook 配置保存失败")));
  };

  return <>
    <div className="notification-settings-page">
      <p className="page-introduction">{t("告警可以发送到 ntfy，或通过 Webhook 交给自己的服务与自动化平台处理。所有通知通道默认关闭，保存配置不会自动启用。")}</p>
      <div className="notification-settings-grid">
    <section className="content-section notification-channel-section">
      <div className="section-heading"><div><h2>{t("ntfy 通知")}</h2><p className="field-hint">{t("令牌只保存在秘密库；停用后不会发送网络请求。")}</p></div></div>
      <form className="form-grid" onSubmit={saveNtfy}>
        <label>{t("服务地址")}<input name="baseUrl" value={ntfy.baseUrl} onChange={(event) => setNtfy({ ...ntfy, baseUrl: event.target.value })} required disabled={!loaded.ntfy} /></label>
        <label>{t("主题")}<input name="topic" value={ntfy.topic} onChange={(event) => setNtfy({ ...ntfy, topic: event.target.value })} required disabled={!loaded.ntfy} /></label>
        <label className="full-field">{t("访问令牌（可选）")}<input name="token" type="password" disabled={!loaded.ntfy || clearToken} /><span className="field-hint">{t(ntfy.hasToken ? "令牌已配置；留空将保留现有令牌" : "尚未配置令牌")}</span></label>
        <label className="full-field checkbox-field"><input type="checkbox" checked={ntfy.enabled} onChange={(event) => setNtfy({ ...ntfy, enabled: event.target.checked })} disabled={!loaded.ntfy} /> {t("启用 ntfy 通知")}</label>
        {ntfy.hasToken && <label className="full-field checkbox-field"><input type="checkbox" checked={clearToken} onChange={(event) => setClearToken(event.target.checked)} disabled={!loaded.ntfy} /> {t("清除已保存令牌")}</label>}
        <ChannelActions saveLabel={t("保存通知配置")} loaded={loaded.ntfy} canTest={ntfy.enabled} testing={testing === "ntfy"} onTest={() => testChannel("ntfy", "/api/ntfy/test")} t={t} />
        {errors.ntfy && <p className="error-message full-field" role="alert">{errors.ntfy}</p>}
      </form>
    </section>

    <section className="content-section notification-channel-section">
      <div className="section-heading"><div><h2>{t("Webhook 通知")}</h2><p className="field-hint">{t("将告警以固定 JSON 格式发送到你的 HTTPS 接口，适合接入自动化平台、告警中心或自建服务。")}</p></div></div>
      <form className="form-grid" onSubmit={saveWebhook}>
        <label className="full-field">{t("Webhook 地址")}<input name="webhookEndpoint" type="url" value={webhook.endpoint} onChange={(event) => setWebhook({ ...webhook, endpoint: event.target.value })} placeholder="https://alerts.example.com/shadoc" required disabled={!loaded.webhook} /></label>
        <label>{t("认证方式")}<select value={webhook.authMode} onChange={(event) => {
          const authMode = event.target.value as WebhookState["authMode"];
          setWebhook({ ...webhook, authMode });
          setClearWebhookSecret(authMode === "none" && webhook.hasSecret);
        }} disabled={!loaded.webhook}><option value="none">{t("不认证")}</option><option value="bearer">Bearer</option><option value="hmac-sha256">HMAC-SHA256</option></select></label>
        <label>{t("认证秘密")}<input name="webhookSecret" type="password" disabled={!loaded.webhook || webhook.authMode === "none" || clearWebhookSecret} required={webhook.authMode !== "none" && !webhook.hasSecret} /><span className="field-hint">{t(webhook.hasSecret ? "认证秘密已配置；留空将保留" : "尚未配置认证秘密")}</span></label>
        <label className="full-field checkbox-field"><input type="checkbox" checked={webhook.enabled} onChange={(event) => setWebhook({ ...webhook, enabled: event.target.checked })} disabled={!loaded.webhook} /> {t("启用 Webhook 通知")}</label>
        {webhook.hasSecret && <label className="full-field checkbox-field"><input type="checkbox" checked={clearWebhookSecret} onChange={(event) => setClearWebhookSecret(event.target.checked)} disabled={!loaded.webhook} /> {t("清除已保存认证秘密")}</label>}
        <ChannelActions saveLabel={t("保存 Webhook")} loaded={loaded.webhook} canTest={webhook.enabled} testing={testing === "webhook"} onTest={() => testChannel("webhook", "/api/webhook/test")} t={t} />
        {errors.webhook && <p className="error-message full-field" role="alert">{errors.webhook}</p>}
      </form>
    </section>
      </div>
    </div>

    <Toast message={message} locale={locale} onClose={() => setMessage("")} />
  </>;
}

function ChannelActions({ saveLabel, loaded, canTest, testing, onTest, t }: { saveLabel: string; loaded: boolean; canTest: boolean; testing: boolean; onTest(): void; t(source: string): string }) {
  return <div className="full-field channel-actions">
    <button className="primary-button" type="submit" disabled={!loaded}>{saveLabel}</button>
    <button className="secondary-button" type="button" disabled={!loaded || !canTest || testing} onClick={onTest}>{t(testing ? "正在发送…" : "发送测试")}</button>
  </div>;
}
