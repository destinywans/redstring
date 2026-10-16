import { API_MODE, ApiError, api, newIdempotencyKey } from "./client";
import { currentLocale } from "../app/preferences";
import { currentEnv } from "../app/environment";
import type { Upload, UploadKind } from "./types";

export const MAX_UPLOAD = 20 * 1024 * 1024;

/**
 * Upload one attachment. Contract: POST /uploads (multipart `file`) → Upload.
 * Links and collapsed pastes are sent as JSON ({ kind, name, url | text }).
 * Attachments are treated as data by the agent; they are never executed.
 */
export function uploadFile(file: File, kind: UploadKind, onProgress: (pct: number) => void): Promise<Upload> {
  if (file.size > MAX_UPLOAD) return Promise.reject(new Error("too_large"));
  if (API_MODE === "mock") {
    return new Promise((resolve, reject) => {
      let pct = 0;
      const t = setInterval(() => {
        pct = Math.min(100, pct + 12 + Math.round(Math.random() * 18));
        onProgress(pct);
        if (pct >= 100) {
          clearInterval(t);
          api<Upload>("POST", "/uploads", { body: { kind, name: file.name, size: file.size, mime: file.type } }).then(resolve, reject);
        }
      }, 120);
    });
  }
  return new Promise((resolve, reject) => {
    const form = new FormData();
    form.append("file", file);
    form.append("kind", kind);
    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/api/v1/uploads");
    xhr.withCredentials = true;
    // Same headers as every other write: the server refuses an upload without one environment (PA-165).
    xhr.setRequestHeader("Accept-Language", currentLocale());
    if (currentEnv()) xhr.setRequestHeader("X-AIOps-Environment", currentEnv()!);
    xhr.setRequestHeader("Idempotency-Key", newIdempotencyKey());
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(Math.round((100 * e.loaded) / e.total));
    xhr.onload = () => {
      if (xhr.status < 300) return resolve(JSON.parse(xhr.responseText) as Upload);
      // Say the server's reason, not just the status.
      let body: { error?: { code?: string; message?: string } } | null = null;
      try {
        body = JSON.parse(xhr.responseText);
      } catch {
        /* not JSON */
      }
      reject(new ApiError(xhr.status, body?.error?.code ?? "request_failed", body?.error?.message ?? `HTTP ${xhr.status}`));
    };
    xhr.onerror = () => reject(new Error("network"));
    xhr.send(form);
  });
}

export function addLink(url: string): Promise<Upload> {
  let name = url;
  try {
    const u = new URL(url);
    name = u.hostname + u.pathname.replace(/\/$/, "");
  } catch {
    /* keep raw text */
  }
  return api<Upload>("POST", "/uploads", { body: { kind: "link", name, url, size: 0, mime: "text/uri-list" } });
}

export function addPaste(text: string): Promise<Upload> {
  const lines = text.split("\n").length;
  return api<Upload>("POST", "/uploads", { body: { kind: "paste", name: `paste-${lines}`, size: new Blob([text]).size, mime: "text/plain", text } });
}

export function formatSize(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** PUT /me/avatar (multipart `file`, PNG/JPEG ≤ 2 MB, ≥ 256 × 256) → `{ url }` of the private avatar content. */
export async function uploadAvatar(file: File): Promise<{ url: string }> {
  if (API_MODE === "mock") return { url: URL.createObjectURL(file) };
  const form = new FormData();
  form.append("file", file);
  const res = await fetch("/api/v1/me/avatar", {
    method: "PUT",
    credentials: "same-origin",
    headers: {
      "Accept-Language": currentLocale(),
      ...(currentEnv() ? { "X-AIOps-Environment": currentEnv() } : {}),
      "Idempotency-Key": newIdempotencyKey(),
    },
    body: form,
  });
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new ApiError(res.status, body?.error?.code ?? "request_failed", body?.error?.message ?? `HTTP ${res.status}`);
  }
  return res.json();
}
