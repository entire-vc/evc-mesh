// Keep the small UUID resolvers in one lazy chunk. Their shared rendering
// compresses together while the login entry still loads none of them.
export { TaskDeepLinkResolver } from "./task-deep-link";
export { DocumentDeepLinkResolver } from "./document-deep-link";
export { ProjectDeepLinkResolver } from "./project-deep-link";
