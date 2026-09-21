import { useRef, type ReactNode } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X } from "../../lib/icons";

/** A single application surface; the list stays in place underneath. */
export default function ApplicationDrawer({
  open,
  onOpenChange,
  title,
  children,
  variant = "form",
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  children: ReactNode;
  variant?: "form" | "details";
}) {
  const previousFocus = useRef<HTMLElement | null>(null);
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="application-drawer-overlay" />
        <Dialog.Content
          className={`application-drawer${variant === "details" ? " application-drawer--details" : ""}`}
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
              previousFocus.current.focus({ preventScroll: true });
            }
          }}
        >
          <header className="application-drawer-header">
            <Dialog.Title>{title}</Dialog.Title>
            <Dialog.Close className="icon-button" aria-label="关闭应用卡片">
              <X size={20} />
            </Dialog.Close>
          </header>
          <div className="application-drawer-body">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
