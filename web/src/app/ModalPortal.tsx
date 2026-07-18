import { type ReactNode } from "react";
import { createPortal } from "react-dom";

export function ModalPortal({ children }: { children: ReactNode }) {
  return createPortal(<div className="dialog-backdrop">{children}</div>, document.body);
}
