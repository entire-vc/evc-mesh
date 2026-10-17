import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { describe, expect, it } from "vitest";
import { ConnectedAppsRedirect } from "../App";

function Where() {
  const loc = useLocation();
  return <div data-testid="where">{`${loc.pathname}${loc.hash}`}</div>;
}

describe("legacy /connected-apps route", () => {
  it("redirects to the App access section of the profile", () => {
    render(
      <MemoryRouter initialEntries={["/w/acme/connected-apps"]}>
        <Routes>
          <Route
            path="w/:wsSlug/connected-apps"
            element={<ConnectedAppsRedirect />}
          />
          <Route path="w/:wsSlug/me" element={<Where />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByTestId("where").textContent).toBe(
      "/w/acme/me#app-access",
    );
  });
});
