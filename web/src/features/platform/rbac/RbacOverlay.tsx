import { useState, type ReactNode } from "react";
import DiscardDialog from "../../../components/patterns/DiscardDialog";
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
      <DiscardDialog
        open={confirmClose}
        onOpenChange={setConfirmClose}
        onDiscard={onClose}
      />
      {typeof children === "function" ? children(close) : children}
    </Sheet>
  );
}
