import dictionary from "../../../shared/search/pinyin.json";

const pinyin: Readonly<Record<string, string>> = dictionary;
const keyCache = new Map<string, readonly [string, string]>();
const maximumCachedKeys = 512;
const maximumCachedTextLength = 4096;
const compact = (value: string) =>
  value.replace(/\s+/gu, "").replace(/ü/g, "v");

/** Keep Chinese characters typed by the user literal, even inside mixed queries. */
function searchKeys(text: string, query: string): readonly [string, string] {
  const literalCharacters = new Set(
    Array.from(query).filter((character) => pinyin[character]),
  );
  const key = `${Array.from(literalCharacters).sort().join("")}\u0000${text}`;
  const cached = keyCache.get(key);
  if (cached) return cached;
  let full = "";
  let initials = "";
  for (const character of text) {
    const reading = literalCharacters.has(character)
      ? undefined
      : pinyin[character];
    full += reading ?? character;
    initials += reading ? reading[0] : character;
  }
  const result = [compact(full), compact(initials)] as const;
  // Bound retained log/evidence text while reusing keys across typing and filtering.
  if (text.length <= maximumCachedTextLength) {
    if (keyCache.size >= maximumCachedKeys) {
      const oldest = keyCache.keys().next().value;
      if (oldest !== undefined) keyCache.delete(oldest);
    }
    keyCache.set(key, result);
  }
  return result;
}

/** Local display-text search; backend-paginated queries remain server-filtered. */
export function matchesSearch(value: string, query: string): boolean {
  const text = value.toLowerCase().trim();
  const needle = query.toLowerCase().trim();
  if (!needle || text.includes(needle)) return true;
  // Chinese-only input must not match unrelated words sharing an initial.
  if (!/[a-z]/.test(needle)) return false;
  const [full, initials] = searchKeys(text, needle);
  const normalizedNeedle = compact(needle);
  return full.includes(normalizedNeedle) || initials.includes(normalizedNeedle);
}

/** cmdk adapter: visible option labels/keywords precede generated option IDs. */
export function searchScore(
  value: string,
  query: string,
  keywords?: string[],
): number {
  const text = (keywords?.length ? keywords.join(" ") : value).toLowerCase();
  const needle = query.trim().toLowerCase();
  if (!needle) return 1;
  if (text.trim() === needle) return 1;
  if (text.startsWith(needle)) return 0.95;
  if (text.includes(needle)) return 0.9;
  return matchesSearch(text, needle) ? 0.75 : 0;
}
