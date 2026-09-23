import { create } from "zustand";
export type ToastKind = "error" | "success" | "info";
export type Toast = { id: number; message: string; kind: ToastKind };
const limit = 4;
let nextId = 1;
export const useToastStore = create<{
  toasts: Toast[];
  push: (message: string, kind?: ToastKind) => void;
  dismiss: (id: number) => void;
}>((set) => ({
  toasts: [],
  // Repeated identical messages collapse into the newest one instead of stacking.
  push: (message, kind = "info") =>
    set(({ toasts }) => ({
      toasts: [
        ...toasts.filter((t) => t.message !== message || t.kind !== kind),
        { id: nextId++, message, kind },
      ].slice(-limit),
    })),
  dismiss: (id) =>
    set(({ toasts }) => ({ toasts: toasts.filter((t) => t.id !== id) })),
}));
