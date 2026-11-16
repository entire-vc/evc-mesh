// w/:ws/initiatives/:id is the initiative page (PRD dashboards §5.8); the list page is retired (§5.9) and its
// address opens the «New dashboard» dialog with the Initiatives template. One route entry with a splat: a
// second entry grew the login chunk past its ceiling.
import { useParams } from "react-router";
import { LegacyPageRedirect } from "@/components/layout/legacy-page-redirect";
import { InitiativeDetailPage } from "./initiative-detail";

export function InitiativesPage() {
  return useParams()["*"] ? <InitiativeDetailPage /> : <LegacyPageRedirect page="initiatives" />;
}
