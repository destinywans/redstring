import { create } from "zustand";

export const useAskAi = create<{ isOpen: boolean; draft: string; setOpen: (v: boolean) => void; toggle: () => void; open: (draft?: string) => void }>((set) => ({
  isOpen: false,
  draft: "",
  setOpen: (isOpen) => set({ isOpen }),
  toggle: () => set((s) => ({ isOpen: !s.isOpen })),
  open: (draft = "") => set({ isOpen: true, draft }),
}));

/** Ask AI card width + gap. From this width up, a floating detail card moves left of it instead of under it. */
export const ASK_COVER = 420 + 12;
export const ASK_SIDE_BY_SIDE = 1280;
export const askCover = (open: boolean) => (open && window.innerWidth >= ASK_SIDE_BY_SIDE ? ASK_COVER : 0);
