/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** "1" only in the perf-counter build (web/perf/). Never set for a shipped bundle. */
  readonly VITE_PERF_PROFILER?: string;
}
