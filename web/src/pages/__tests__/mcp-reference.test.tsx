import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { McpReference } from "@/pages/mcp-reference";

describe("McpReference", () => {
  it("always renders the endpoint and the .mcp.json snippet, with no toggle", () => {
    render(<McpReference />);
    expect(screen.getByText("MCP Server")).toBeInTheDocument();
    expect(screen.getByText("Claude Code / Cline — .mcp.json")).toBeInTheDocument();
    expect(screen.getByText(`${window.location.origin}/mcp/sse`)).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });
});
