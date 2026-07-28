import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { InstancePanel } from "./InstancePanel";
import type { components } from "../api/schema";

type TeamInstance = components["schemas"]["TeamInstance"];

const start = { mutate: vi.fn(), isPending: false, error: null as unknown };
const stop = { mutate: vi.fn(), isPending: false };
const extend = { mutate: vi.fn(), isPending: false };

vi.mock("../api/hooks", () => ({
  useStartInstance: () => start,
  useStopInstance: () => stop,
  useExtendInstance: () => extend,
}));

function panel(instance: TeamInstance | null) {
  return render(<InstancePanel slug="ch" instance={instance} />);
}

describe("InstancePanel", () => {
  it("shows Start when there is no instance", () => {
    panel(null);
    expect(screen.getByTestId("instance-start")).toBeInTheDocument();
    expect(screen.queryByTestId("instance-stop")).not.toBeInTheDocument();
  });

  it("shows connection info, countdown, Extend, and Stop when running", () => {
    const future = new Date(Date.now() + 3_600_000).toISOString();
    panel({
      id: "i1",
      state: "running",
      host_port: 30000,
      connection_info: "http://ctf:30000",
      started_at: new Date().toISOString(),
      expires_at: future,
      error: null,
    });
    expect(screen.getByText("http://ctf:30000")).toBeInTheDocument();
    expect(screen.getByTestId("instance-countdown")).toBeInTheDocument();
    expect(screen.getByTestId("instance-extend")).toBeInTheDocument();
    expect(screen.getByTestId("instance-stop")).toBeInTheDocument();
  });

  it("shows Retry on error", () => {
    panel({ id: "i1", state: "error", error: "boom" } as TeamInstance);
    expect(screen.getByText("boom")).toBeInTheDocument();
    expect(screen.getByTestId("instance-start")).toHaveTextContent("Retry");
  });
});
