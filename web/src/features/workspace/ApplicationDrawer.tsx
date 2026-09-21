import type { ReactNode } from "react";
import { Sheet } from "../../components/ui/sheet";

/** Compatibility adapter for the shared, accessible application side sheet. */
export default function ApplicationDrawer({
  open,
  onOpenChange,
  title,
  description,
  children,
  variant = "form",
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: ReactNode;
  variant?: "form" | "details";
}) {
  return (
    <Sheet
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      description={description}
      size={variant === "details" ? "detail" : "form"}
      className={`application-sheet application-sheet--${variant}`}
      closeLabel="关闭应用卡片"
    >
      {children}
    </Sheet>
  );
}
