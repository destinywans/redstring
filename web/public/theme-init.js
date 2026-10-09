// Apply the saved palette before the first paint; theme motion is for changes only.
// External (not inline) so the server CSP `script-src 'self'` allows it.
(() => {
  let preference = "system";
  try {
    preference = localStorage.getItem("aiops-theme") || "system";
  } catch {}
  const dark =
    preference === "dark" ||
    (preference !== "light" &&
      matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
  let v3 = "system";
  try {
    v3 = JSON.parse(localStorage.getItem("aiops-v3-preferences") || "{}").theme || "system";
  } catch {}
  document.documentElement.dataset.v3Theme =
    v3 === "dark" || (v3 !== "light" && matchMedia("(prefers-color-scheme: dark)").matches) ? "dark" : "light";
  document.querySelector('meta[name="theme-color"]').content = dark
    ? "#030405"
    : "#f7f8fa";
})();
