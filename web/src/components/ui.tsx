import type { ReactNode, ButtonHTMLAttributes } from "react";
import { useRef, useId } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X, Pin, Minus, MoreHorizontal } from "../lib/icons";
import { Button as SharedButton } from "./ui/button";
import { clsx } from "clsx";
import { twMerge } from "tailwind-merge";
export const cn = (...values: Parameters<typeof clsx>) => twMerge(clsx(values));
export function Button({
  variant = "default",
  className,
  type = "submit",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "default" | "outline" | "ghost";
}) {
  // Legacy forms rely on the native submit default; visual behavior is shared.
  return (
    <SharedButton
      variant={variant}
      type={type}
      className={cn(
        "btn",
        variant === "default" && "btn-primary",
        variant === "ghost" && "btn-ghost",
        className,
      )}
      {...props}
    />
  );
}
export function Modal({
  open,
  onOpenChange,
  title,
  children,
  description,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  title: string;
  description?: string;
  children: ReactNode;
}) {
  const previousFocus = useRef<HTMLElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const descriptionId = useId();
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          ref={contentRef}
          className="dialog-content"
          aria-describedby={description ? descriptionId : undefined}
          onOpenAutoFocus={(event) => {
            previousFocus.current =
              document.activeElement instanceof HTMLElement
                ? document.activeElement
                : null;
            const input = contentRef.current?.querySelector<
              HTMLInputElement | HTMLTextAreaElement
            >(
              'input:not([disabled]):not([type="hidden"]), textarea:not([disabled])',
            );
            if (input) {
              event.preventDefault();
              input.focus();
            }
          }}
          onCloseAutoFocus={(event) => {
            if (previousFocus.current?.isConnected) {
              event.preventDefault();
              previousFocus.current.focus({ preventScroll: true });
            }
          }}
        >
          <Dialog.Title>{title}</Dialog.Title>
          {description && (
            <Dialog.Description id={descriptionId}>
              {description}
            </Dialog.Description>
          )}
          <Dialog.Close className="icon-button dialog-close" aria-label="关闭">
            <X size={18} />
          </Dialog.Close>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
export function CardHeading({
  title,
  icon,
  children,
}: {
  title: ReactNode;
  icon?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="card-heading">
      <h2>
        {icon}
        {title}
      </h2>
      {children}
    </div>
  );
}
export function CardTools({
  onCollapse,
  onPin,
  pinned = false,
}: {
  onCollapse?: () => void;
  onPin?: () => void;
  pinned?: boolean;
}) {
  return (
    <div className="card-tools">
      {onPin && (
        <button
          className={cn("icon-button", pinned && "active")}
          title={pinned ? "取消固定" : "固定卡片"}
          onClick={onPin}
        >
          <Pin size={15} />
        </button>
      )}
      {onCollapse && (
        <button className="icon-button" title="折叠卡片" onClick={onCollapse}>
          <Minus size={16} />
        </button>
      )}
    </div>
  );
}
export function ErrorNotice({ error }: { error: unknown }) {
  return (
    <div className="error-notice" role="alert">
      {error instanceof Error ? error.message : String(error)}
    </div>
  );
}
