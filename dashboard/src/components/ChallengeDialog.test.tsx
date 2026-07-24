import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ChallengeDialog } from "./ChallengeDialog";
import { ToastProvider } from "./ui/toast";

// Mock the API hooks so the dialog renders without a backend.
const submitMock = vi.fn();
vi.mock("../api/hooks", () => ({
  useChallenge: () => ({
    data: {
      id: "1",
      slug: "sanity",
      title: "Sanity Check",
      category: "misc",
      points: 50,
      solves: 3,
      solved_by_me: false,
      kind: "standard",
      has_instance: false,
      description: "Submit the flag.",
      attachments: [],
      attempts_used: 0,
      max_attempts: null,
      connection_info: null,
    },
    isLoading: false,
  }),
  useSubmitFlag: () => ({
    mutate: submitMock,
    isPending: false,
  }),
}));

function renderDialog() {
  const qc = new QueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <ChallengeDialog slug="sanity" onClose={() => undefined} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe("ChallengeDialog", () => {
  beforeEach(() => {
    submitMock.mockReset();
  });

  it("renders the challenge and a flag input", () => {
    renderDialog();
    expect(screen.getByText("Sanity Check")).toBeInTheDocument();
    expect(screen.getByTestId("flag-input")).toBeInTheDocument();
  });

  it("shows a success message and points when the flag is correct", async () => {
    submitMock.mockImplementation((_flag: string, opts: { onSuccess: (r: unknown) => void }) => {
      opts.onSuccess({ correct: true, points: 50 });
    });
    renderDialog();
    fireEvent.change(screen.getByTestId("flag-input"), { target: { value: "OSCTF{x}" } });
    fireEvent.click(screen.getByTestId("flag-submit"));
    await waitFor(() => {
      expect(screen.getByText(/Correct/)).toBeInTheDocument();
    });
    expect(screen.getByText(/\+50 pts/)).toBeInTheDocument();
  });

  it("shows an error for an incorrect flag", async () => {
    submitMock.mockImplementation((_flag: string, opts: { onSuccess: (r: unknown) => void }) => {
      opts.onSuccess({ correct: false, points: null });
    });
    renderDialog();
    fireEvent.change(screen.getByTestId("flag-input"), { target: { value: "wrong" } });
    fireEvent.click(screen.getByTestId("flag-submit"));
    await waitFor(() => {
      expect(screen.getByText(/Incorrect flag/)).toBeInTheDocument();
    });
  });
});
