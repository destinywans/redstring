import { useState, type ReactNode } from "react";
import { Sheet } from "../../../components/ui/sheet";
export function RbacOverlay({
  title,
  subtitle,
  wide,
  dirty = false,
  busy = false,
  onClose,
  children,
}: {
  title: string;
  subtitle?: string;
  wide?: boolean;
  dirty?: boolean;
  busy?: boolean;
  onClose: () => void;
  children: ReactNode | ((requestClose: () => void) => ReactNode);
}) {
  const [confirmClose, setConfirmClose] = useState(false);
  const close = () => {
    if (busy) return;
    if (dirty) setConfirmClose(true);
    else onClose();
  };
  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open) close();
      }}
      title={title}
      description={subtitle}
      size={wide ? "wide" : "form"}
      className={`rbac-overlay ${wide ? "rbac-overlay--wide" : "rbac-overlay--card"}`}
      bodyLayout="structured"
      closeDisabled={busy}
      closeLabel="关闭"
    >
      {confirmClose && (
        <div className="rbac-unsaved" role="alert">
          <span>有尚未保存的修改，关闭后将丢失。</span>
          <button className="btn" onClick={() => setConfirmClose(false)}>
            继续编辑
          </button>
          <button className="btn directory-danger" onClick={onClose}>
            放弃修改并关闭
          </button>
        </div>
      )}
      {typeof children === "function" ? children(close) : children}
    </Sheet>
  );
}
