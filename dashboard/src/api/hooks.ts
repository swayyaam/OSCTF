import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, normalizeError, queryKeys, RequestError } from "./client";
import type { components } from "./schema";

type Schemas = components["schemas"];

interface FetchResult<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** unwrap throws a RequestError on failure, else returns the (possibly empty) body. */
async function unwrap<T>(p: Promise<FetchResult<T>>): Promise<T> {
  const { data, error, response } = await p;
  if (!response.ok) {
    throw new RequestError(normalizeError(response.status, error));
  }
  return data as T;
}

// --- queries ---------------------------------------------------------------

export function useMe() {
  return useQuery({
    queryKey: queryKeys.me,
    queryFn: () => unwrap<Schemas["Me"]>(api.GET("/auth/me")),
    retry: false,
    staleTime: 30_000,
  });
}

export function useEvent() {
  return useQuery({
    queryKey: queryKeys.event,
    queryFn: () => unwrap<Schemas["Event"]>(api.GET("/event")),
  });
}

export function useChallenges(enabled = true) {
  return useQuery({
    queryKey: queryKeys.challenges,
    queryFn: () => unwrap<Schemas["Challenge"][]>(api.GET("/challenges")),
    enabled,
  });
}

export function useChallenge(slug: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.challenge(slug),
    queryFn: () =>
      unwrap<Schemas["ChallengeDetail"]>(
        api.GET("/challenges/{slug}", { params: { path: { slug } } }),
      ),
    enabled,
    // Poll while a per-team instance is settling or running, so the panel catches
    // pending->running transitions and TTL expiry without a manual refresh.
    refetchInterval: (query) => {
      const st = query.state.data?.instance?.state;
      if (st === "pending" || st === "starting") return 2500;
      if (st === "running") return 20_000;
      return false;
    },
  });
}

export function useScoreboard() {
  return useQuery({
    queryKey: queryKeys.scoreboard,
    queryFn: () => unwrap<Schemas["Scoreboard"]>(api.GET("/scoreboard")),
  });
}

export function useTeams() {
  return useQuery({
    queryKey: queryKeys.teams,
    queryFn: () => unwrap<Schemas["TeamSummary"][]>(api.GET("/teams")),
  });
}

export function usePublicTeam(id: string) {
  return useQuery({
    queryKey: queryKeys.team(id),
    queryFn: () =>
      unwrap<Schemas["TeamDetail"]>(api.GET("/teams/{id}", { params: { path: { id } } })),
  });
}

export function usePublicUser(id: string) {
  return useQuery({
    queryKey: queryKeys.user(id),
    queryFn: () =>
      unwrap<Schemas["PublicUser"]>(api.GET("/users/{id}", { params: { path: { id } } })),
  });
}

// --- auth mutations --------------------------------------------------------

export function useRegister() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["RegisterRequest"]) =>
      unwrap<Schemas["Me"]>(api.POST("/auth/register", { body })),
    onSuccess: (me) => qc.setQueryData(queryKeys.me, me),
  });
}

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["LoginRequest"]) =>
      unwrap<Schemas["Me"]>(api.POST("/auth/login", { body })),
    onSuccess: (me) => qc.setQueryData(queryKeys.me, me),
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST("/auth/logout")),
    onSuccess: () => {
      qc.setQueryData(queryKeys.me, null);
      void qc.invalidateQueries();
    },
  });
}

export function useChangePassword() {
  return useMutation({
    mutationFn: (body: Schemas["ChangePasswordRequest"]) =>
      unwrap(api.PATCH("/auth/me/password", { body })),
  });
}

// --- team mutations --------------------------------------------------------

function invalidateMe(qc: ReturnType<typeof useQueryClient>) {
  void qc.invalidateQueries({ queryKey: queryKeys.me });
  void qc.invalidateQueries({ queryKey: queryKeys.teams });
}

export function useCreateTeam() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["TeamCreateRequest"]) =>
      unwrap<Schemas["TeamMine"]>(api.POST("/teams", { body })),
    onSuccess: () => { invalidateMe(qc); },
  });
}

export function useJoinTeam() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["TeamJoinRequest"]) =>
      unwrap<Schemas["TeamMine"]>(api.POST("/teams/join", { body })),
    onSuccess: () => { invalidateMe(qc); },
  });
}

export function useLeaveTeam() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST("/teams/leave")),
    onSuccess: () => { invalidateMe(qc); },
  });
}

export function useRenameTeam(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["TeamRenameRequest"]) =>
      unwrap<Schemas["TeamMine"]>(api.PATCH("/teams/{id}", { params: { path: { id } }, body })),
    onSuccess: () => { invalidateMe(qc); },
  });
}

export function useRegenInvite(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      unwrap<Schemas["InviteCode"]>(
        api.POST("/teams/{id}/invite-code", { params: { path: { id } } }),
      ),
    onSuccess: () => { invalidateMe(qc); },
  });
}

// --- per-team instances ----------------------------------------------------

function useInstanceMutation(slug: string, action: () => Promise<unknown>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: action,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.challenge(slug) });
      void qc.invalidateQueries({ queryKey: queryKeys.challenges });
    },
  });
}

export function useStartInstance(slug: string) {
  return useInstanceMutation(slug, () =>
    unwrap<Schemas["TeamInstance"]>(
      api.POST("/challenges/{slug}/instance", { params: { path: { slug } } }),
    ),
  );
}

export function useStopInstance(slug: string) {
  return useInstanceMutation(slug, () =>
    unwrap(api.DELETE("/challenges/{slug}/instance", { params: { path: { slug } } })),
  );
}

export function useExtendInstance(slug: string) {
  return useInstanceMutation(slug, () =>
    unwrap<Schemas["TeamInstance"]>(
      api.POST("/challenges/{slug}/instance/extend", { params: { path: { slug } } }),
    ),
  );
}

// --- submission ------------------------------------------------------------

export function useSubmitFlag(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (flag: string) =>
      unwrap<Schemas["SubmitResult"]>(
        api.POST("/challenges/{slug}/submit", { params: { path: { slug } }, body: { flag } }),
      ),
    onSuccess: (res) => {
      if (res.correct) {
        void qc.invalidateQueries({ queryKey: queryKeys.challenges });
        void qc.invalidateQueries({ queryKey: queryKeys.challenge(slug) });
        void qc.invalidateQueries({ queryKey: queryKeys.scoreboard });
        void qc.invalidateQueries({ queryKey: queryKeys.me });
      }
    },
  });
}
