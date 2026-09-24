import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const client = new QueryClient({
  defaultOptions: {
    queries: { retry: false, staleTime: 15000, refetchOnWindowFocus: false },
    mutations: { retry: false },
  },
});

/** v3 UI is the default app; the previous UI stays reachable under /legacy until the new API contract is served. */
const legacy = window.location.pathname === "/legacy" || window.location.pathname.startsWith("/legacy/");

async function boot() {
  let tree: React.ReactNode;
  if (legacy) {
    await Promise.all([import("./typography.css"), import("./styles.css"), import("./ui-baseline.css")]);
    const { default: App } = await import("./App");
    tree = (
      <BrowserRouter basename="/legacy">
        <App />
      </BrowserRouter>
    );
  } else {
    const { default: V3App } = await import("./v3/app/V3App");
    tree = (
      <BrowserRouter>
        <V3App />
      </BrowserRouter>
    );
  }
  ReactDOM.createRoot(document.getElementById("root")!).render(
    <React.StrictMode>
      <QueryClientProvider client={client}>{tree}</QueryClientProvider>
    </React.StrictMode>,
  );
}
void boot();
