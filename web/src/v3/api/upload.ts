import { API_MODE, api } from "./client";
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
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(Math.round((100 * e.loaded) / e.total));
    xhr.onload = () => (xhr.status < 300 ? resolve(JSON.parse(xhr.responseText) as Upload) : reject(new Error(`HTTP ${xhr.status}`)));
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
