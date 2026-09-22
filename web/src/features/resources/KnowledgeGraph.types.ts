import type { ResourceGraph, ResourceOption } from "./types";

export type KnowledgeGroup = {
  id: string;
  label: string;
  virtual: true;
  source_id: string;
  source_kind: string;
  cluster: string;
  namespace: string;
  count: number;
  resource_kinds: ResourceOption[];
};
export type KnowledgeGroupEdge = {
  source_group_id: string;
  target_group_id: string;
  kind: string;
  count: number;
  virtual: true;
};
export type KnowledgeGraphResponse = ResourceGraph & {
  mode: string;
  root_id?: string;
  total: number;
  offset: number;
  limit: number;
  remaining: number;
  next_offset: number | null;
  groups: KnowledgeGroup[];
  group_edges: KnowledgeGroupEdge[];
  facets: {
    resource_kinds: ResourceOption[];
    relation_kinds: ResourceOption[];
  };
  limits: {
    resources: number;
    relationships: number;
    nodes: number;
    edges: number;
  };
  warnings: string[];
  page_node_ids?: string[];
};
export type KnowledgeNode = {
  id: string;
  name: string;
  kind: string;
  subtitle: string;
  group?: boolean;
  count?: number;
};
export type KnowledgeEdge = {
  id: string;
  source: string;
  target: string;
  label: string;
  declared?: boolean;
};
