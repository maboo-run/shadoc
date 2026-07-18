import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { translate, type Locale } from "../i18n";

const autoCloseMilliseconds = 4500;

export function Toast({ message, locale = "zh-CN", onClose }: { message: string; locale?: Locale; onClose(): void }) {
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    if (!message) return;
    const timer = window.setTimeout(() => closeRef.current(), autoCloseMilliseconds);
    return () => window.clearTimeout(timer);
  }, [message]);
  if (!message) return null;
  return createPortal(
    <div className="toast" role="status" aria-live="polite" aria-atomic="true">
      <span className="toast-message">{message}</span>
      <button className="toast-close" type="button" aria-label={translate(locale, "关闭通知")} title={translate(locale, "关闭通知")} onClick={onClose}>×</button>
      <span key={message} className="toast-progress" aria-hidden="true" style={{ animationDuration: `${autoCloseMilliseconds}ms` }} />
    </div>,
    document.body,
  );
}
