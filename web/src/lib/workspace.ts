import { createContext, useContext } from "react";
import type { Application, Model, User } from "./types";
export type Workspace = {
  preview: boolean;
  user: User;
  applications: Application[];
  application: Application | null;
  setApplication: (id: string) => void;
  models: Model[];
  investigationId: string | null;
  setInvestigationId: (id: string | null) => void;
  notify: (message: string, kind?: "error" | "success" | "info") => void;
};
export const WorkspaceContext = createContext<Workspace | null>(null);
export function useWorkspace() {
  const value = useContext(WorkspaceContext);
  if (!value) throw new Error("Workspace is unavailable");
  return value;
}
