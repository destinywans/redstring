import { useRef, useState, type ReactNode } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X } from "../../../lib/icons";
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
  const previousFocus = useRef<HTMLElement | null>(null);
  const close = () => {
    if (busy) return;
    if (dirty) setConfirmClose(true);
    else onClose();
  };
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) close();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="rbac-shade" />
        <Dialog.Content
          className={`rbac-overlay ${wide ? "rbac-overlay--wide" : "rbac-overlay--card"}`}
          aria-describedby={undefined}
          onOpenAutoFocus={() => {
            previousFocus.current =
              document.activeElement instanceof HTMLElement
                ? document.activeElement
                : null;
          }}
          onCloseAutoFocus={(event) => {
            if (previousFocus.current?.isConnected) {
              event.preventDefault();
              previousFocus.current.focus();
            }
          }}
        >
          <header className="rbac-overlay-heading">
            <div>
              <Dialog.Title>{title}</Dialog.Title>
              {subtitle && <p>{subtitle}</p>}
            </div>
            <button
              type="button"
              className="icon-button"
              aria-label="关闭"
              disabled={busy}
              onClick={close}
            >
              <X size={20} />
            </button>
          </header>
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
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
