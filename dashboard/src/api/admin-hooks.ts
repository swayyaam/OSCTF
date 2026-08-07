import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { API_BASE, api, normalizeError, queryKeys, RequestError } from "./client";
import type { components } from "./schema";

type Schemas = components["schemas"];

interface FetchResult<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

async function unwrap<T>(p: Promise<FetchResult<T>>): Promise<T> {
  const { data, error, response } = await p;
  if (!response.ok) throw new RequestError(normalizeError(response.status, error));
  return data as T;
}

export function useAdminStats() {
  return useQuery({
    queryKey: queryKeys.adminStats,
    queryFn: () => unwrap<Schemas["AdminStats"]>(api.GET("/admin/stats")),
    refetchInterval: 15_000,
  });
}

export function useAdminEvent() {
  return useQuery({
    queryKey: queryKeys.adminEvent,
    queryFn: () => unwrap<Schemas["EventAdmin"]>(api.GET("/admin/event")),
  });
}

export function useUpdateEvent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["EventUpdateRequest"]) =>
      unwrap<Schemas["EventAdmin"]>(api.PATCH("/admin/event", { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.adminEvent });
      void qc.invalidateQueries({ queryKey: queryKeys.event });
      void qc.invalidateQueries({ queryKey: queryKeys.scoreboard });
    },
  });
}

export interface ChallengeFilters {
  page?: number;
  per_page?: number;
  category?: Schemas["Category"];
  visible?: boolean;
  kind?: Schemas["ChallengeKind"];
}

export function useAdminChallenges(filters: ChallengeFilters) {
  return useQuery({
    queryKey: queryKeys.adminChallenges(filters),
    queryFn: () =>
      unwrap<Schemas["ChallengeAdminPage"]>(api.GET("/admin/challenges", { params: { query: filters } })),
  });
}

export function useAdminChallenge(id: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.adminChallenge(id),
    queryFn: () =>
      unwrap<Schemas["ChallengeAdmin"]>(
        api.GET("/admin/challenges/{id}", { params: { path: { id } } }),
      ),
    enabled,
  });
}

export function useCreateChallenge() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["ChallengeAdminCreate"]) =>
      unwrap<Schemas["ChallengeAdmin"]>(api.POST("/admin/challenges", { body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["admin", "challenges"] }),
  });
}

export function useUpdateChallenge(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["ChallengeAdminUpdate"]) =>
      unwrap<Schemas["ChallengeAdmin"]>(
        api.PATCH("/admin/challenges/{id}", { params: { path: { id } }, body }),
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["admin", "challenges"] });
      void qc.invalidateQueries({ queryKey: queryKeys.adminChallenge(id) });
      void qc.invalidateQueries({ queryKey: queryKeys.challenges });
    },
  });
}

export function useDeleteChallenge() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, confirm }: { id: string; confirm?: boolean }) =>
      unwrap(
        api.DELETE("/admin/challenges/{id}", {
          params: { path: { id }, query: confirm ? { confirm: true } : {} },
        }),
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["admin", "challenges"] }),
  });
}

// --- instances -------------------------------------------------------------

export function useInstance(id: string, enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.adminInstance(id),
    queryFn: async () => {
      const { data, error, response } = await api.GET("/admin/challenges/{id}/instance", {
        params: { path: { id } },
      });
      if (response.status === 404) return null;
      if (!response.ok) throw new RequestError(normalizeError(response.status, error));
      return data as Schemas["Instance"];
    },
    enabled,
    refetchInterval: 10_000,
  });
}

function useInstanceAction(id: string, action: () => Promise<unknown>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: action,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.adminInstance(id) });
      void qc.invalidateQueries({ queryKey: queryKeys.adminChallenge(id) });
    },
  });
}

export function useDeployInstance(id: string) {
  return useInstanceAction(id, () =>
    unwrap(api.POST("/admin/challenges/{id}/instance", { params: { path: { id } } })),
  );
}

export function useRestartInstance(id: string) {
  return useInstanceAction(id, () =>
    unwrap(api.POST("/admin/challenges/{id}/instance/restart", { params: { path: { id } } })),
  );
}

export function useDestroyInstance(id: string) {
  return useInstanceAction(id, () =>
    unwrap(api.DELETE("/admin/challenges/{id}/instance", { params: { path: { id } } })),
  );
}

// --- instance fleet (v0.2) -------------------------------------------------

export function useAdminInstances() {
  return useQuery({
    queryKey: queryKeys.adminInstances,
    queryFn: () =>
      unwrap<Schemas["AdminInstanceList"]>(api.GET("/admin/instances")),
    refetchInterval: 5_000,
  });
}

export function useAdminDestroyInstanceById() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      unwrap(api.DELETE("/admin/instances/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.adminInstances });
    },
  });
}

export async function fetchInstanceLogs(id: string, tail: number): Promise<string> {
  const logs = await unwrap<Schemas["InstanceLogs"]>(
    api.GET("/admin/challenges/{id}/instance/logs", {
      params: { path: { id }, query: { tail } },
    }),
  );
  return logs.logs;
}

// --- attachments -----------------------------------------------------------

export function useUploadAttachment(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (file: File) => {
      const form = new FormData();
      form.append("file", file);
      const res = await fetch(`${API_BASE}/admin/challenges/${id}/attachments`, {
        method: "POST",
        body: form,
        credentials: "same-origin",
        headers: { Origin: window.location.origin },
      });
      if (!res.ok) {
        const body: unknown = await res.json().catch(() => ({}));
        throw new RequestError(normalizeError(res.status, body));
      }
    },
    onSuccess: () => void qc.invalidateQueries({ queryKey: queryKeys.adminChallenge(id) }),
  });
}

export function useDeleteAttachment(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (aid: string) =>
      unwrap(
        api.DELETE("/admin/challenges/{id}/attachments/{aid}", {
          params: { path: { id, aid } },
        }),
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: queryKeys.adminChallenge(id) }),
  });
}

// --- users / teams / submissions ------------------------------------------

export interface UserFilters {
  page?: number;
  per_page?: number;
  q?: string;
  banned?: boolean;
  hidden?: boolean;
  role?: Schemas["Role"];
}

export function useAdminUsers(filters: UserFilters) {
  return useQuery({
    queryKey: queryKeys.adminUsers(filters),
    queryFn: () =>
      unwrap<Schemas["UserAdminPage"]>(api.GET("/admin/users", { params: { query: filters } })),
  });
}

export function useUpdateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: Schemas["UserAdminUpdate"] }) =>
      unwrap<Schemas["UserAdmin"]>(api.PATCH("/admin/users/{id}", { params: { path: { id } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["admin", "users"] });
      void qc.invalidateQueries({ queryKey: queryKeys.scoreboard });
    },
  });
}

export function useResetPassword() {
  return useMutation({
    mutationFn: ({ id, new_password }: { id: string; new_password: string }) =>
      unwrap(api.POST("/admin/users/{id}/password", { params: { path: { id } }, body: { new_password } })),
  });
}

export function useAdminTeams(filters: { page?: number; per_page?: number; q?: string }) {
  return useQuery({
    queryKey: queryKeys.adminTeams(filters),
    queryFn: () =>
      unwrap<Schemas["TeamAdminPage"]>(api.GET("/admin/teams", { params: { query: filters } })),
  });
}

export function useUpdateTeam() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: Schemas["TeamAdminUpdate"] }) =>
      unwrap<Schemas["TeamAdmin"]>(api.PATCH("/admin/teams/{id}", { params: { path: { id } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["admin", "teams"] });
      void qc.invalidateQueries({ queryKey: queryKeys.scoreboard });
    },
  });
}

export interface SubmissionFilters {
  page?: number;
  per_page?: number;
  challenge_id?: string;
  team_id?: string;
  user_id?: string;
  correct?: boolean;
  since?: string;
  until?: string;
}

export function useAdminSubmissions(filters: SubmissionFilters, refetch: boolean) {
  return useQuery({
    queryKey: queryKeys.adminSubmissions(filters),
    queryFn: () =>
      unwrap<Schemas["SubmissionAdminPage"]>(
        api.GET("/admin/submissions", { params: { query: filters } }),
      ),
    refetchInterval: refetch ? 10_000 : false,
  });
}
