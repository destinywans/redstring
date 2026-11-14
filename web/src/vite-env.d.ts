/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_MODE?: "mock" | "http";
  readonly VITE_ENABLE_LEGACY_DELIVERY?: "true" | "false";
  readonly VITE_DEMO_ABOUT_URL?: string;
  readonly VITE_DEMO_ABOUT_URL_EN?: string;
  readonly VITE_EMBED_ORIGINS?: string;
}
