/**
 * Sample postmortem details (PA-128), kept in memory so the demo can walk draft → review → published; edits last until
 * the page reloads. It registers no routes: the create and knowledge mocks both use it, and route order stays theirs.
 */
import type { L } from "../i18n";

type Text = string | L;
export type SamplePostmortem = {
  id: string;
  incident_id: string;
  status: "draft" | "review" | "published";
  revision: number;
  source_revision: string;
  sections: { key: string; title: Text; text: Text }[];
  actions: { text: Text; owner: string; due: Text }[];
  authored_by_me: boolean;
  author: string;
  created_at: string;
  updated_at: string;
  incident: { id: string; title: Text; severity: string; started_at: string | null; closed_at: string; duration: string | null };
  submitted: { by: string; at: string } | null;
  published: { by: string; at: string } | null;
  returned: { by: string; at: string; reason: string } | null;
};

/** The sample viewer (see the /me mock). */
export const SAMPLE_ME = "王晨";

export const samplePostmortems = new Map<string, SamplePostmortem>();

export function rememberPostmortem(p: SamplePostmortem) {
  samplePostmortems.set(p.id, p);
}

export const nowIso = () => new Date().toISOString();
