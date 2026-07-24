import { describe, expect, it } from "vitest";
import { normalizeError } from "./client";

describe("normalizeError", () => {
  it("maps a problem+json body to an ApiError", () => {
    const err = normalizeError(422, {
      title: "Validation failed",
      detail: "One or more fields are invalid.",
      errors: { username: ["too short"] },
      request_id: "abc123",
    });
    expect(err.status).toBe(422);
    expect(err.title).toBe("Validation failed");
    expect(err.fields?.username).toEqual(["too short"]);
    expect(err.requestId).toBe("abc123");
  });

  it("falls back to a generic title when the body is empty", () => {
    const err = normalizeError(500, null);
    expect(err.status).toBe(500);
    expect(err.title).toBe("Something went wrong");
    expect(err.fields).toBeUndefined();
  });
});
