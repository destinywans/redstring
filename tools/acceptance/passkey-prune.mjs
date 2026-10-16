#!/usr/bin/env node
/**
 * Prune the acceptance passkeys of one role account (WAN-434 phase 1).
 *
 *   node tools/acceptance/passkey-prune.mjs <prod|nonprod> <role> [--dry-run]
 *
 * Every passkey approval run (web/e2e/golden-approval.spec.ts) registers a fresh passkey with a throwaway virtual
 * authenticator, so the approver accounts pile them up. Past 64, Chrome refuses the server's excludeCredentials /
 * allowCredentials and no approval can run. The old private keys are gone, so the browser can't remove them; this
 * script does it without one: it signs in, registers one software passkey through the real WebAuthn ceremony
 * ("none" attestation, ES256, user verified), uses it to confirm the deletion of every passkey whose label starts
 * with "验收", and finally deletes itself. Only the product's own /me/passkeys routes are called.
 */
import { createHash, generateKeyPairSync, randomBytes, randomUUID, sign } from "node:crypto";
import { readFileSync } from "node:fs";
import { request } from "node:https";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const ORIGINS = { prod: "https://prod.redstring.test:18443", nonprod: "https://nonprod.redstring.test:18444" };
const ENVS = { prod: "prod", nonprod: "test" };
const [instance, role] = process.argv.slice(2);
const dryRun = process.argv.includes("--dry-run");
if (!ORIGINS[instance] || !role) {
  console.error("usage: passkey-prune.mjs <prod|nonprod> <role> [--dry-run]");
  process.exit(2);
}
const origin = ORIGINS[instance];
const account = JSON.parse(readFileSync(resolve(ROOT, ".local/acceptance", instance, "secrets.json"), "utf8")).accounts[role];

const b64url = (b) => Buffer.from(b).toString("base64url");
const unb64url = (s) => Buffer.from(s, "base64url");
const sha256 = (b) => createHash("sha256").update(b).digest();

/** Minimal CBOR for the shapes WebAuthn needs: ints, byte and text strings, maps. */
function cbor(v) {
  const head = (major, n) => {
    if (n < 24) return Buffer.from([(major << 5) | n]);
    if (n < 256) return Buffer.from([(major << 5) | 24, n]);
    if (n < 65536) return Buffer.from([(major << 5) | 25, n >> 8, n & 255]);
    const b = Buffer.alloc(5);
    b[0] = (major << 5) | 26;
    b.writeUInt32BE(n, 1);
    return b;
  };
  if (Number.isInteger(v)) return v >= 0 ? head(0, v) : head(1, -1 - v);
  if (Buffer.isBuffer(v)) return Buffer.concat([head(2, v.length), v]);
  if (typeof v === "string") {
    const s = Buffer.from(v, "utf8");
    return Buffer.concat([head(3, s.length), s]);
  }
  if (v instanceof Map) return Buffer.concat([head(5, v.size), ...[...v].flatMap(([k, x]) => [cbor(k), cbor(x)])]);
  throw new Error(`cbor: unsupported ${typeof v}`);
}

let cookie = "";
const url = new URL(origin);
const ca = readFileSync(resolve(ROOT, ".local/acceptance/tls/cert.pem"));
/** Like tools/acceptance/api.py: *.redstring.test goes to 127.0.0.1 for this process only, trusting only the acceptance certificate. */
function call(method, path, body) {
  const payload = body !== undefined ? JSON.stringify(body) : undefined;
  return new Promise((done, fail) => {
    const req = request(
      {
        host: "127.0.0.1", port: Number(url.port), servername: url.hostname, ca, method, path: "/api/v1" + path,
        headers: {
          Host: url.host,
          Origin: origin,
          "Accept-Language": "zh-CN",
          "X-AIOps-Environment": ENVS[instance],
          ...(cookie ? { Cookie: cookie } : {}),
          ...(payload !== undefined ? { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(payload) } : {}),
          ...(method !== "GET" ? { "Idempotency-Key": randomUUID() } : {}),
        },
      },
      (res) => {
        const session = (res.headers["set-cookie"] ?? []).find((c) => c.startsWith("aiops_session="));
        if (session) cookie = session.split(";")[0];
        let text = "";
        res.setEncoding("utf8");
        res.on("data", (d) => (text += d));
        res.on("end", () => {
          let json = null;
          try {
            json = text ? JSON.parse(text) : null;
          } catch {
            json = text;
          }
          done({ status: res.statusCode, json, retryAfter: Number(res.headers["retry-after"] ?? 0) });
        });
      },
    );
    req.on("error", fail);
    if (payload !== undefined) req.write(payload);
    req.end();
  });
}

const login = await call("POST", "/auth/login", { email: account.email, password: account.password });
if (login.status !== 200) throw new Error(`login ${login.status}`);
const listed = await call("GET", "/me/passkeys");
const creds = listed.json?.credentials ?? [];
// Acceptance passkeys, plus cleanup keys an interrupted earlier run left behind.
const targets = creds.filter((c) => /^(验收|清理用临时密钥)/.test(String(c.label ?? "")));
console.log(`${instance}/${role}: ${creds.length} passkeys, ${targets.length} labelled 验收…`);
if (dryRun || !targets.length) process.exit(0);

// One software passkey, registered through the real ceremony.
const { privateKey, publicKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
const jwk = publicKey.export({ format: "jwk" });
const credId = randomBytes(32);
const reg = await call("POST", "/me/passkeys/registration/options");
if (reg.status >= 300) throw new Error(`registration options ${reg.status} ${JSON.stringify(reg.json)}`);
const pk = reg.json.options.publicKey;
const cose = cbor(new Map([[1, 2], [3, -7], [-1, 1], [-2, unb64url(jwk.x)], [-3, unb64url(jwk.y)]]));
const lenBuf = Buffer.alloc(2);
lenBuf.writeUInt16BE(credId.length);
const regAuthData = Buffer.concat([sha256(pk.rp.id), Buffer.from([0x45]), Buffer.alloc(4), Buffer.alloc(16), lenBuf, credId, cose]);
const regClient = Buffer.from(JSON.stringify({ type: "webauthn.create", challenge: pk.challenge, origin, crossOrigin: false }));
const attestation = cbor(new Map([["fmt", "none"], ["attStmt", new Map()], ["authData", regAuthData]]));
const fin = await call("POST", "/me/passkeys/registration/finish", {
  challenge_id: reg.json.id,
  label: "清理用临时密钥",
  credential: {
    id: b64url(credId), rawId: b64url(credId), type: "public-key", clientExtensionResults: {},
    response: { attestationObject: b64url(attestation), clientDataJSON: b64url(regClient), transports: ["internal"] },
  },
});
if (fin.status >= 300) throw new Error(`registration finish ${fin.status} ${JSON.stringify(fin.json)}`);
const userHandle = pk.user.id;

let counter = 0;
const pause = (ms) => new Promise((r) => setTimeout(r, ms));
/** The passkey routes are rate limited: wait as the server says (or 20 s) and try again. */
async function patient(method, path, body) {
  for (let i = 0; ; i++) {
    const r = await call(method, path, body);
    if (r.status !== 429 || i >= 30) return r;
    await pause(1000 * (r.retryAfter || 20));
  }
}
async function remove(targetId) {
  const o = await patient("POST", "/me/passkeys/deletion/options", { credential_id: targetId });
  if (o.status >= 300) return `options ${o.status}`;
  const req = o.json.options.publicKey;
  counter += 1;
  const count = Buffer.alloc(4);
  count.writeUInt32BE(counter);
  const authData = Buffer.concat([sha256(req.rpId), Buffer.from([0x05]), count]);
  const client = Buffer.from(JSON.stringify({ type: "webauthn.get", challenge: req.challenge, origin, crossOrigin: false }));
  const signature = sign("sha256", Buffer.concat([authData, sha256(client)]), privateKey);
  const f = await patient("POST", "/me/passkeys/deletion/finish", {
    challenge_id: o.json.id,
    credential: {
      id: b64url(credId), rawId: b64url(credId), type: "public-key", clientExtensionResults: {},
      response: { authenticatorData: b64url(authData), clientDataJSON: b64url(client), signature: b64url(signature), userHandle },
    },
  });
  return f.status < 300 ? "ok" : `finish ${f.status} ${JSON.stringify(f.json).slice(0, 160)}`;
}

let removed = 0;
for (const t of targets) {
  const r = await remove(t.id);
  if (r === "ok") removed += 1;
  else console.log(`  ${t.id.slice(0, 10)}…: ${r}`);
}
const self = await remove(b64url(credId));
const after = await call("GET", "/me/passkeys");
console.log(`${instance}/${role}: removed ${removed}/${targets.length}; cleanup key ${self}; ${after.json?.credentials?.length ?? "?"} left`);
