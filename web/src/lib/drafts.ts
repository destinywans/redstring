import { create } from "zustand";
export type CitationDraft = {
  investigationId: string;
  branchId?: string;
  documentId: string;
  title: string;
  version: number;
  body: string;
};
export type PromptDraft = {
  investigationId: string;
  branchId: string;
  content: string;
  clientRequestId?: string;
};
export const useDraftStore = create<{
  pendingCitation: CitationDraft | null;
  setCitation: (value: CitationDraft | null) => void;
  pendingPrompt: PromptDraft | null;
  setPrompt: (value: PromptDraft | null) => void;
}>((set) => ({
  pendingCitation: null,
  setCitation: (pendingCitation) => set({ pendingCitation }),
  pendingPrompt: null,
  setPrompt: (pendingPrompt) => set({ pendingPrompt }),
}));
