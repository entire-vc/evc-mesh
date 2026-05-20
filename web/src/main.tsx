import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { Toaster, toast } from "@/components/ui/toast";
import { useAuthStore } from "@/stores/auth";
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
  (!savedTheme && window.matchMedia("(prefers-color-scheme: dark)").matches)
) {
  document.documentElement.classList.add("dark");
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
    <Toaster />
  </StrictMode>,
);
