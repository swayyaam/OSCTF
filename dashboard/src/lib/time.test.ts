import { describe, expect, it } from "vitest";
import { formatRelative } from "./time";

describe("formatRelative", () => {
  it("renders past times with an 'ago' suffix", () => {
    const twoMinAgo = new Date(Date.now() - 2 * 60_000).toISOString();
    expect(formatRelative(twoMinAgo)).toBe("2m ago");
  });

  it("renders future times with an 'in' prefix", () => {
    const inThreeHours = new Date(Date.now() + 3 * 3600_000).toISOString();
    expect(formatRelative(inThreeHours)).toBe("in 3h");
  });

  it("uses the largest fitting unit", () => {
    const inTwoDays = new Date(Date.now() + 2 * 86400_000 + 5000).toISOString();
    expect(formatRelative(inTwoDays)).toBe("in 2d");
  });
});
