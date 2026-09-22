const MAX_UPLOAD_BYTES = 20 * 1024 * 1024;
const MAX_PIXELS = 32 * 1024 * 1024;
const MAX_EDGE = 16384;
const AVATAR_EDGE = 512;
const MAX_RESULT_BYTES = 1024 * 1024;

// Read dimensions before decoding: a tiny compressed image must not trigger an
// unbounded pixel allocation. Only the raster formats accepted by the picker
// are supported; SVG and unknown content never reach a decoder.
export function avatarDimensions(bytes: Uint8Array): {
  width: number;
  height: number;
} {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const text = (offset: number, size: number) =>
    String.fromCharCode(...bytes.subarray(offset, offset + size));
  let width = 0;
  let height = 0;
  if (
    bytes.length >= 24 &&
    bytes[0] === 137 &&
    text(1, 7) === "PNG\r\n\x1a\n" &&
    text(12, 4) === "IHDR"
  ) {
    width = view.getUint32(16);
    height = view.getUint32(20);
  } else if (bytes.length >= 10 && ["GIF87a", "GIF89a"].includes(text(0, 6))) {
    width = view.getUint16(6, true);
    height = view.getUint16(8, true);
  } else if (
    bytes.length >= 25 &&
    text(0, 4) === "RIFF" &&
    text(8, 4) === "WEBP"
  ) {
    const chunk = text(12, 4);
    if (chunk === "VP8X" && bytes.length >= 30) {
      width = 1 + bytes[24] + (bytes[25] << 8) + (bytes[26] << 16);
      height = 1 + bytes[27] + (bytes[28] << 8) + (bytes[29] << 16);
    } else if (
      chunk === "VP8 " &&
      bytes.length >= 30 &&
      bytes[23] === 0x9d &&
      bytes[24] === 0x01 &&
      bytes[25] === 0x2a
    ) {
      width = view.getUint16(26, true) & 0x3fff;
      height = view.getUint16(28, true) & 0x3fff;
    } else if (chunk === "VP8L" && bytes[20] === 0x2f) {
      width = 1 + (((bytes[22] & 0x3f) << 8) | bytes[21]);
      height =
        1 + (((bytes[24] & 0x0f) << 10) | (bytes[23] << 2) | (bytes[22] >> 6));
    }
  } else if (bytes.length >= 4 && bytes[0] === 0xff && bytes[1] === 0xd8) {
    let offset = 2;
    while (offset + 3 < bytes.length) {
      if (bytes[offset++] !== 0xff) break;
      while (offset < bytes.length && bytes[offset] === 0xff) offset++;
      const marker = bytes[offset++];
      if (marker === 0xd9 || marker === 0xda) break;
      if (marker === 0x01 || (marker >= 0xd0 && marker <= 0xd7)) continue;
      if (offset + 2 > bytes.length) break;
      const length = view.getUint16(offset);
      if (length < 2 || offset + length > bytes.length) break;
      if (
        marker >= 0xc0 &&
        marker <= 0xcf &&
        ![0xc4, 0xc8, 0xcc].includes(marker) &&
        length >= 8
      ) {
        height = view.getUint16(offset + 3);
        width = view.getUint16(offset + 5);
        break;
      }
      offset += length;
    }
  }
  if (!width || !height)
    throw new Error("无法读取这张图片，请选择 JPG、PNG、WebP 或 GIF 图片。");
  if (width > MAX_EDGE || height > MAX_EDGE || width * height > MAX_PIXELS) {
    throw new Error("这张图片尺寸过大，请换一张普通照片。");
  }
  return { width, height };
}

export async function prepareAvatar(file: File): Promise<string> {
  if (file.size > MAX_UPLOAD_BYTES)
    throw new Error("这张图片过大，请选择小于 20 MB 的图片。");
  const bytes = new Uint8Array(await file.arrayBuffer());
  avatarDimensions(bytes);
  let bitmap: ImageBitmap;
  try {
    bitmap = await createImageBitmap(file);
  } catch {
    throw new Error("无法打开这张图片，请换一张图片重试。");
  }
  const canvas = document.createElement("canvas");
  try {
    if (
      !bitmap.width ||
      !bitmap.height ||
      bitmap.width > MAX_EDGE ||
      bitmap.height > MAX_EDGE ||
      bitmap.width * bitmap.height > MAX_PIXELS
    ) {
      throw new Error("这张图片尺寸过大，请换一张普通照片。");
    }
    const sourceEdge = Math.min(bitmap.width, bitmap.height);
    canvas.width = canvas.height = Math.min(AVATAR_EDGE, sourceEdge);
    const context = canvas.getContext("2d");
    if (!context) throw new Error("图片处理暂时不可用，请刷新页面重试。");
    context.imageSmoothingQuality = "high";
    context.drawImage(
      bitmap,
      (bitmap.width - sourceEdge) / 2,
      (bitmap.height - sourceEdge) / 2,
      sourceEdge,
      sourceEdge,
      0,
      0,
      canvas.width,
      canvas.height,
    );
    const encode = (type: string, quality?: number) =>
      new Promise<Blob>((resolve, reject) => {
        canvas.toBlob(
          (blob) =>
            blob
              ? resolve(blob)
              : reject(new Error("图片处理失败，请换一张图片重试。")),
          type,
          quality,
        );
      });
    let blob = await encode("image/png");
    if (blob.size > MAX_RESULT_BYTES) {
      context.globalCompositeOperation = "destination-over";
      context.fillStyle = "#ffffff";
      context.fillRect(0, 0, canvas.width, canvas.height);
      blob = await encode("image/jpeg", 0.85);
    }
    if (blob.size > MAX_RESULT_BYTES)
      throw new Error("图片处理失败，请换一张图片重试。");
    return await new Promise<string>((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () =>
        typeof reader.result === "string"
          ? resolve(reader.result)
          : reject(new Error("图片读取失败，请重试。"));
      reader.onerror = () => reject(new Error("图片读取失败，请重试。"));
      reader.readAsDataURL(blob);
    });
  } finally {
    bitmap.close();
    canvas.width = canvas.height = 0;
  }
}
