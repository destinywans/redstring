import { useId, useRef, type ReactNode } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X } from "../../lib/icons";
import { cn } from "../../lib/utils";
import { Button } from "./button";
import "./system.css";

export interface SheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  size?: "form" | "detail" | "wide";
  className?: string;
  closeLabel?: string;
}

export function Sheet({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  size = "form",
  className,
  closeLabel = "关闭卡片",
}: SheetProps) {
  const previousFocus = useRef<HTMLElement | null>(null);
  const descriptionId = useId();
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="ui-sheet-overlay" />
        <Dialog.Content
          className={cn("ui-sheet", `ui-sheet-${size}`, className)}
          aria-describedby={description ? descriptionId : undefined}
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
          <header className="ui-sheet-header">
            <div className="ui-sheet-heading">
              <Dialog.Title className="ui-sheet-title">{title}</Dialog.Title>
              {description && (
                <Dialog.Description
                  id={descriptionId}
                  className="ui-sheet-description"
                >
                  {description}
                </Dialog.Description>
              )}
            </div>
            <Dialog.Close asChild>
              <Button variant="ghost" size="icon" aria-label={closeLabel}>
                <X size={18} />
              </Button>
            </Dialog.Close>
          </header>
          <div className="ui-sheet-body">{children}</div>
          {footer && <footer className="ui-sheet-footer">{footer}</footer>}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
