import { Profiler, type ProfilerOnRenderCallback, type ReactNode } from "react";

/**
 * React commit counter for the perf храповик (web/perf/, [perf·A] #e0f9cb61).
 *
 * Exists ONLY in a build made with VITE_PERF_PROFILER=1. Vite replaces
 * `import.meta.env.VITE_PERF_PROFILER` with a literal at build time, so in a
 * normal build `PERF_PROFILER` is the constant `false`, the Profiler branch is
 * dead code and the minifier drops it together with the callback below.
 * `perf-bundle` proves that on every pipeline: `grep -c onRender` over the
 * production dist must stay 0.
 *
 * React's regular production build never calls onRender at all, so the perf
 * build also aliases react-dom/client → react-dom/profiling (vite.config.ts).
 * Without that alias every counter would read 0 and every gate would pass.
 */
export const PERF_PROFILER = import.meta.env.VITE_PERF_PROFILER === "1";

type PerfCounters = Record<string, number>;

declare global {
  interface Window {
    /** Commits per Profiler id since the last reset. Read by web/perf/*.spec.ts. */
    __meshPerfCommits?: PerfCounters;
  }
}

const onRender: ProfilerOnRenderCallback = (id) => {
  const counters = (window.__meshPerfCommits ??= {});
  counters[id] = (counters[id] ?? 0) + 1;
};

export function PerfProfiler({ id, children }: { id: string; children: ReactNode }) {
  if (!PERF_PROFILER) return <>{children}</>;
  return (
    <Profiler id={id} onRender={onRender}>
      {children}
    </Profiler>
  );
}
