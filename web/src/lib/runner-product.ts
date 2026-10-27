/**
 * The Runner as the user sees it: one product. Its catalog entry is the runner
 * program from Spark (`agent-runner`, set up on the operator's host and
 * connected by its address); the built-in `runner` extension is the server side
 * of the same product and never shows up in the extension lists. Everything a
 * user does with the Runner, the program included, happens on the Runner
 * screen, so the extension screens only point there.
 */
export const RUNNER_PRODUCT_ID = "agent-runner";

/** The built-in server side (resource catalog, controllers, leadership). */
export const RUNNER_SERVER_ID = "runner";

/** The workspace setting that names where the runner program answers. */
export const RUNNER_URL_SETTING = "runner_url";

export const isRunnerProduct = (id: string) => id === RUNNER_PRODUCT_ID;

export const runnerScreenPath = (wsSlug: string) => `/w/${wsSlug}/runner`;
