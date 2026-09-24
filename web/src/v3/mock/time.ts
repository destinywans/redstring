/** Fixture clock: the demo incident starts at 14:03 local time today, so relative times stay meaningful. */
const base = new Date();
base.setHours(14, 31, 0, 0);
export const NOW = base.getTime() > Date.now() ? Date.now() : base.getTime();
/** ISO timestamp `minutes` before the fixture "now". */
export const ago = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString();
/** ISO timestamp for HH:MM today (fixture day). */
export const at = (hhmm: string, dayOffset = 0) => {
  const [h, m] = hhmm.split(":").map(Number);
  const d = new Date(NOW);
  d.setDate(d.getDate() + dayOffset);
  d.setHours(h, m, 0, 0);
  return d.toISOString();
};
