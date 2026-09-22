import type { KnowledgeEdge, KnowledgeNode } from "./KnowledgeGraph.types";

type Point = [number, number];
type Body = {
  id: string;
  point: Point;
  home: Point;
  pinned: boolean;
};

// Includes the circle and its two/three-line label, rather than just the icon.
const LABEL_WIDTH = 184;
const GOLDEN_ANGLE = Math.PI * (3 - Math.sqrt(5));

function phase(id: string) {
  let hash = 0;
  for (const char of id) hash = (Math.imul(hash, 31) + char.charCodeAt(0)) | 0;
  return ((hash >>> 0) / 4294967296) * Math.PI * 2;
}

function adjacency(nodes: KnowledgeNode[], edges: KnowledgeEdge[]) {
  const neighbors = new Map(nodes.map((node) => [node.id, new Set<string>()]));
  for (const edge of edges) {
    if (edge.source === edge.target) continue;
    if (!neighbors.has(edge.source) || !neighbors.has(edge.target)) continue;
    neighbors.get(edge.source)!.add(edge.target);
    neighbors.get(edge.target)!.add(edge.source);
  }
  return neighbors;
}

function components(neighbors: Map<string, Set<string>>, rootId: string) {
  const unseen = new Set([...neighbors.keys()].sort());
  const result: string[][] = [];
  while (unseen.size) {
    const queue = [unseen.values().next().value!];
    unseen.delete(queue[0]);
    for (let index = 0; index < queue.length; index++) {
      for (const next of [...neighbors.get(queue[index])!].sort()) {
        if (unseen.delete(next)) queue.push(next);
      }
    }
    result.push(queue);
  }
  return result.sort(
    (a, b) =>
      Number(b.includes(rootId)) - Number(a.includes(rootId)) ||
      b.length - a.length ||
      a[0].localeCompare(b[0]),
  );
}

/** Seed each actual connected component around its most connected real node.
 * BFS branches receive angular sectors; broad stars use staggered radii so they
 * do not become one unreadably crowded circumference. No synthetic edges/root. */
function seedComponent(
  ids: string[],
  neighbors: Map<string, Set<string>>,
  rootId: string,
  center: Point,
): Body[] {
  const hub = ids.includes(rootId)
    ? rootId
    : [...ids].sort(
        (a, b) =>
          neighbors.get(b)!.size - neighbors.get(a)!.size || a.localeCompare(b),
      )[0];
  const children = new Map(ids.map((id) => [id, [] as string[]]));
  const order = [hub];
  const visited = new Set(order);
  for (let index = 0; index < order.length; index++) {
    for (const next of [...neighbors.get(order[index])!].sort()) {
      if (visited.has(next)) continue;
      visited.add(next);
      children.get(order[index])!.push(next);
      order.push(next);
    }
  }
  const weights = new Map<string, number>();
  for (const id of [...order].reverse()) {
    weights.set(
      id,
      Math.max(
        1,
        children.get(id)!.reduce((sum, child) => sum + weights.get(child)!, 0),
      ),
    );
  }
  const bodies: Body[] = [];
  const offset = phase(hub);
  const visit = (
    id: string,
    depth: number,
    start: number,
    span: number,
    sibling: number,
  ) => {
    const angle = start + span / 2;
    const radius =
      depth === 0
        ? 0
        : 155 * depth +
          (depth === 1 && children.get(hub)!.length > 9
            ? (sibling % 2) * 135
            : 0);
    bodies.push({
      id,
      point: [
        center[0] + Math.cos(angle) * radius * 1.15,
        center[1] + Math.sin(angle) * radius * 0.9,
      ],
      home: center,
      pinned: id === rootId,
    });
    let cursor = start;
    children.get(id)!.forEach((child, index) => {
      const childSpan = (span * weights.get(child)!) / weights.get(id)!;
      visit(child, depth + 1, cursor, childSpan, index);
      cursor += childSpan;
    });
  };
  visit(hub, 0, offset, Math.PI * 2, 0);
  return bodies;
}

function separate(bodies: Body[], labelHeight: number, rounds: number) {
  for (let round = 0; round < rounds; round++) {
    let overlaps = 0;
    for (let i = 0; i < bodies.length; i++) {
      for (let j = i + 1; j < bodies.length; j++) {
        const a = bodies[i];
        const b = bodies[j];
        const dx = b.point[0] - a.point[0];
        const dy = b.point[1] - a.point[1];
        const overlapX = LABEL_WIDTH - Math.abs(dx);
        const overlapY = labelHeight - Math.abs(dy);
        if (overlapX <= 0 || overlapY <= 0 || (a.pinned && b.pinned)) continue;
        overlaps++;
        const axis = overlapX / LABEL_WIDTH < overlapY / labelHeight ? 0 : 1;
        const distance = axis === 0 ? dx : dy;
        const amount =
          ((axis === 0 ? overlapX : overlapY) + 0.5) *
          Math.sign(distance || (i % 2 ? -1 : 1));
        const share = a.pinned || b.pinned ? 1 : 0.5;
        if (!a.pinned) a.point[axis] -= amount * share;
        if (!b.pinned) b.point[axis] += amount * share;
      }
    }
    if (!overlaps) break;
  }
}

/** Bounded, synchronous spring relaxation. There is no persistent simulation or
 * animation: a later refresh/expansion does not restart this initial layout. */
function relax(bodies: Body[], edges: KnowledgeEdge[], labelHeight: number) {
  const index = new Map(bodies.map((body, i) => [body.id, i]));
  const seen = new Set<string>();
  const links: [number, number][] = [];
  for (const edge of edges) {
    const a = index.get(edge.source);
    const b = index.get(edge.target);
    if (a === undefined || b === undefined || a === b) continue;
    const key = [Math.min(a, b), Math.max(a, b)].join(":");
    if (!seen.has(key)) links.push([a, b]);
    seen.add(key);
  }
  for (let step = 0; step < 130; step++) {
    const forces: Point[] = bodies.map((body) => [
      (body.home[0] - body.point[0]) * 0.008 - body.point[0] * 0.04,
      (body.home[1] - body.point[1]) * 0.008 - body.point[1] * 0.13,
    ]);
    for (let i = 0; i < bodies.length; i++) {
      for (let j = i + 1; j < bodies.length; j++) {
        let dx = (bodies[j].point[0] - bodies[i].point[0]) / LABEL_WIDTH;
        let dy = (bodies[j].point[1] - bodies[i].point[1]) / labelHeight;
        if (dx === 0 && dy === 0) {
          dx = Math.cos(phase(bodies[j].id)) * 0.01;
          dy = Math.sin(phase(bodies[j].id)) * 0.01;
        }
        const distance = Math.hypot(dx, dy);
        if (distance > 2.6) continue;
        const strength = Math.min(18, 7 / Math.max(0.2, distance * distance));
        const fx = (dx / distance) * strength;
        const fy = (dy / distance) * strength;
        forces[i][0] -= fx;
        forces[i][1] -= fy;
        forces[j][0] += fx;
        forces[j][1] += fy;
      }
    }
    for (const [i, j] of links) {
      const dx = bodies[j].point[0] - bodies[i].point[0];
      const dy = bodies[j].point[1] - bodies[i].point[1];
      const length = Math.hypot(dx, dy) || 1;
      const strength = (length - 185) * 0.018;
      const fx = (dx / length) * strength;
      const fy = (dy / length) * strength;
      forces[i][0] += fx;
      forces[i][1] += fy;
      forces[j][0] -= fx;
      forces[j][1] -= fy;
    }
    const maxStep = 9 - step / 20;
    bodies.forEach((body, i) => {
      if (body.pinned) return;
      const length = Math.hypot(...forces[i]);
      const scale = Math.min(1, maxStep / Math.max(1, length));
      body.point[0] += forces[i][0] * scale;
      body.point[1] += forces[i][1] * scale;
    });
    separate(bodies, labelHeight, 1);
  }
  separate(bodies, labelHeight, 80);
  // Dense initial graphs can leave a few collisions after bounded relaxation.
  // Resolve only those nodes locally; never run an unbounded simulation.
  const occupied: Point[] = [];
  for (const body of [...bodies].sort(
    (a, b) => Number(b.pinned) - Number(a.pinned),
  )) {
    const clear = ([x, y]: Point) =>
      occupied.every(
        ([px, py]) =>
          Math.abs(px - x) >= LABEL_WIDTH || Math.abs(py - y) >= labelHeight,
      );
    if (!body.pinned && !clear(body.point)) {
      const origin: Point = [...body.point];
      for (let attempt = 1; attempt <= 10000; attempt++) {
        const angle = phase(body.id) + attempt * GOLDEN_ANGLE;
        const radius = Math.sqrt(attempt) * 24;
        const candidate: Point = [
          origin[0] + Math.cos(angle) * radius * 1.4,
          origin[1] + Math.sin(angle) * radius,
        ];
        if (clear(candidate)) {
          body.point = candidate;
          break;
        }
      }
      if (!clear(body.point))
        body.point = [
          Math.max(...occupied.map(([x]) => x)) + LABEL_WIDTH,
          origin[1],
        ];
    }
    occupied.push(body.point);
  }
}

function addNodes(
  nodes: KnowledgeNode[],
  neighbors: Map<string, Set<string>>,
  positions: Map<string, Point>,
  anchorId: string,
  rootId: string,
  labelHeight: number,
) {
  const pending = nodes.filter((node) => !positions.has(node.id));
  const clear = ([x, y]: Point) =>
    [...positions.values()].every(
      ([px, py]) =>
        Math.abs(px - x) >= LABEL_WIDTH || Math.abs(py - y) >= labelHeight,
    );
  while (pending.length) {
    // Prefer the next connected node; newly placed parents can anchor children.
    const connected = pending.findIndex((node) =>
      [...neighbors.get(node.id)!].some((id) => positions.has(id)),
    );
    const node = pending.splice(connected < 0 ? 0 : connected, 1)[0];
    const adjacent = [...neighbors.get(node.id)!].filter((id) =>
      positions.has(id),
    );
    const anchor = positions.get(
      adjacent.includes(anchorId) ? anchorId : adjacent[0],
    ) ??
      positions.get(anchorId) ??
      positions.get(rootId) ?? [0, 0];
    const offset = phase(node.id);
    let candidate: Point = [0, 0];
    // Ring capacity grows with circumference. With <=300 occupied positions,
    // this finite spiral has substantially more space than can be occupied.
    let placed = false;
    for (let ring = 1; ring <= 40 && !placed; ring++) {
      const slots = 8 * ring;
      for (let slot = 0; slot < slots; slot++) {
        const angle =
          offset + (slot * Math.PI * 2) / slots + ring * GOLDEN_ANGLE;
        candidate = [
          anchor[0] + Math.cos(angle) * ring * 205,
          anchor[1] + Math.sin(angle) * ring * labelHeight * 1.65,
        ];
        if (clear(candidate)) {
          placed = true;
          break;
        }
      }
    }
    if (!placed)
      candidate = [
        Math.max(...[...positions.values()].map(([x]) => x)) + LABEL_WIDTH,
        anchor[1],
      ];
    positions.set(node.id, candidate);
  }
}

export function placeKnowledgeNodes(
  nodes: KnowledgeNode[],
  edges: KnowledgeEdge[],
  positions: Map<string, Point>,
  rootId: string,
  anchorId: string,
  width: number,
  height: number,
): void {
  const active = new Set(nodes.map((node) => node.id));
  for (const id of positions.keys()) if (!active.has(id)) positions.delete(id);
  if (!nodes.length) return;
  const neighbors = adjacency(nodes, edges);
  const labelHeight = nodes.some((node) => node.group) ? 124 : 106;
  if (positions.size) {
    addNodes(nodes, neighbors, positions, anchorId, rootId, labelHeight);
    return;
  }
  const clusters = components(neighbors, rootId);
  const aspect = Math.max(1, Math.min(1.9, width / Math.max(1, height)));
  const bodies = clusters.flatMap((ids, index) => {
    const angle = index * GOLDEN_ANGLE;
    const radius = index === 0 ? 0 : 160 * Math.sqrt(index);
    const center: Point = [
      Math.cos(angle) * radius * Math.sqrt(aspect),
      (Math.sin(angle) * radius) / Math.sqrt(aspect),
    ];
    return seedComponent(ids, neighbors, rootId, center);
  });
  relax(bodies, edges, labelHeight);
  if (!active.has(rootId)) {
    // Center the actual drawing (including asymmetric disconnected clusters).
    const x =
      (Math.min(...bodies.map((body) => body.point[0])) +
        Math.max(...bodies.map((body) => body.point[0]))) /
      2;
    const y =
      (Math.min(...bodies.map((body) => body.point[1])) +
        Math.max(...bodies.map((body) => body.point[1]))) /
      2;
    for (const body of bodies) {
      body.point[0] -= x;
      body.point[1] -= y;
    }
  }
  for (const body of bodies) positions.set(body.id, body.point);
}
