import { useEffect, useState } from "react";
import { getInstanceOverview } from "@/lib/extension-instance";

/** The instance administrator is not the workspace admin: only the instance API can tell. */
export function useIsInstanceAdmin(): boolean {
  const [ok, setOk] = useState(false);
  useEffect(() => {
    let live = true;
    getInstanceOverview().then(() => live && setOk(true), () => {});
    return () => {
      live = false;
    };
  }, []);
  return ok;
}
