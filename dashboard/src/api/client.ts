import createClient, { type Middleware } from "openapi-fetch";
import type { paths } from "./schema";

/**
 * The single API client. Same-origin in production; the Vite dev proxy forwards
 * /api to the Go server. Cookies (the session) ride along automatically.
 */
export const api = createClient<paths>({
  baseUrl: "/api/v0",
  credentials: "same-origin",
});

/** A normalized problem+json error surfaced to the UI. */
export interface ApiError {
  status: number;
  title: string;
  detail?: string;
  /** Field name -> messages, for form validation. */
  fields?: Record<string, string[]>;
  requestId?: string;
}

interface ProblemBody {
  title?: string;
  detail?: string;
  errors?: Record<string, string[]>;
  request_id?: string;
}

/** normalizeError converts an openapi-fetch error payload into an ApiError. */
export function normalizeError(status: number, body: unknown): ApiError {
  const p = (body ?? {}) as ProblemBody;
  return {
    status,
    title: p.title ?? "Something went wrong",
    detail: p.detail,
    fields: p.errors,
    requestId: p.request_id,
  };
}

/** Thrown by the query/mutation wrappers when a request fails. */
export class RequestError extends Error {
  readonly api: ApiError;
  constructor(apiErr: ApiError) {
    super(apiErr.detail ?? apiErr.title);
    this.name = "RequestError";
    this.api = apiErr;
  }
}

let onUnauthorized: (() => void) | null = null;

/** setUnauthorizedHandler wires the global 401 handler (redirect to login). */
export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn;
}

// A middleware that triggers the global 401 handler on protected reads.
const authMiddleware: Middleware = {
  onResponse({ response }) {
    if (response.status === 401 && onUnauthorized) {
      onUnauthorized();
    }
    return response;
  },
};
api.use(authMiddleware);

/** Query-key factory keeps cache keys consistent across hooks. */
export const queryKeys = {
  me: ["me"] as const,
  event: ["event"] as const,
  challenges: ["challenges"] as const,
  challenge: (slug: string) => ["challenge", slug] as const,
  scoreboard: ["scoreboard"] as const,
  teams: ["teams"] as const,
  team: (id: string) => ["team", id] as const,
  user: (id: string) => ["user", id] as const,
  myTeam: ["me", "team"] as const,
  adminEvent: ["admin", "event"] as const,
  adminChallenges: (filters?: unknown) => ["admin", "challenges", filters] as const,
  adminChallenge: (id: string) => ["admin", "challenge", id] as const,
  adminInstance: (id: string) => ["admin", "instance", id] as const,
  adminInstances: ["admin", "instances"] as const,
  adminUsers: (filters?: unknown) => ["admin", "users", filters] as const,
  adminTeams: (filters?: unknown) => ["admin", "teams", filters] as const,
  adminSubmissions: (filters?: unknown) => ["admin", "submissions", filters] as const,
  adminStats: ["admin", "stats"] as const,
};
