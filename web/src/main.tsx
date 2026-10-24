import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/i18n";
import { App } from "./App";
import { Toaster, toast } from "@/components/ui/toast";
import { useAuthStore } from "@/stores/auth";
import { captureStaticShell } from "@/lib/static-shell";
import { PerfProfiler } from "@/lib/perf-profiler";
import { watchSystemMode } from "@/core/themes/mode";
import "./index.css";

// Initialize auth state
useAuthStore.getState().initialize();

// Register service worker for PWA offline support, and prompt the user when
// a new deploy is available so an open tab picks up new bundles without
// requiring a manual hard-refresh.
if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker
      .register("/sw.js")
      .then((registration) => {
        const showUpdatePrompt = () => {
          toast.info("A new version of Mesh is available", {
            description: "Reload to get the latest changes.",
            action: {
              label: "Reload",
              onClick: () => window.location.reload(),
            },
            duration: Infinity,
          });
        };

        // Periodic update check — catches deploys for users sitting on an
        // open tab without navigation. 60s is a good trade-off between
        // freshness and noise.
        setInterval(() => {
          registration.update().catch(() => {});
        }, 60_000);

        registration.addEventListener("updatefound", () => {
          const newWorker = registration.installing;
          if (!newWorker) return;
          newWorker.addEventListener("statechange", () => {
            if (
              newWorker.state === "installed" &&
              navigator.serviceWorker.controller
            ) {
              showUpdatePrompt();
            }
          });
        });

        // SW already waiting at registration time (e.g. tab reopened after
        // deploy without a reload) — prompt immediately.
        if (registration.waiting && navigator.serviceWorker.controller) {
          showUpdatePrompt();
        }
      })
      .catch(() => {});
  });
}

// Restore theme preference
const savedTheme = localStorage.getItem("theme");
if (
  savedTheme === "dark" ||
  (savedTheme !== "light" && window.matchMedia("(prefers-color-scheme: dark)").matches)
) {
  document.documentElement.classList.add("dark");
}

// `system` follows the OS on every screen, the sign-in one included.
watchSystemMode();

// index.html may have painted a static shell into #root; keep what was typed
// into it before React replaces it (see lib/static-shell.ts).
const rootEl = document.getElementById("root")!;
captureStaticShell(rootEl);

createRoot(rootEl).render(
  <StrictMode>
    <PerfProfiler id="app">
      <App />
    </PerfProfiler>
    <Toaster />
  </StrictMode>,
);
