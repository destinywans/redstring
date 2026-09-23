import { useEffect, useState } from "react";
import { X } from "../lib/icons";
import { useToastStore, type Toast } from "../lib/toast";

const duration = { error: 10_000, success: 5_000, info: 5_000 };

function ToastItem({ toast }: { toast: Toast }) {
  const dismiss = useToastStore((s) => s.dismiss);
  const [paused, setPaused] = useState(false);
  useEffect(() => {
    if (paused) return;
    const timer = setTimeout(() => dismiss(toast.id), duration[toast.kind]);
    return () => clearTimeout(timer);
  }, [paused, toast.id, toast.kind, dismiss]);
  return (
    <div
      className={`toast ${toast.kind}`}
      role={toast.kind === "error" ? "alert" : undefined}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
    >
      <span>{toast.message}</span>
      <button
        className="icon-button"
        aria-label="关闭通知"
        onClick={() => dismiss(toast.id)}
      >
        <X size={16} />
      </button>
    </div>
  );
}

/** The live region stays mounted so screen readers announce each new toast. */
export default function Toaster() {
  const toasts = useToastStore((s) => s.toasts);
  return (
    <div
      className="toast-stack"
      role="region"
      aria-label="通知"
      aria-live="polite"
      aria-relevant="additions"
    >
      {toasts.map((toast) => (
        <ToastItem key={toast.id} toast={toast} />
      ))}
    </div>
  );
}
