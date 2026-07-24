import { useEffect, useState, type SyntheticEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  useAdminChallenge,
  useCreateChallenge,
  useDeleteChallenge,
  useUpdateChallenge,
} from "../../api/admin-hooks";
import { RequestError } from "../../api/client";
import type { components } from "../../api/schema";
import { useToast } from "../../components/ui/toast";
import { AdminNav } from "./AdminNav";
import { InstancePanel } from "./InstancePanel";
import { AttachmentsPanel } from "./AttachmentsPanel";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { Card, FieldError, Label, Skeleton } from "../../components/ui/misc";

type ChallengeAdmin = components["schemas"]["ChallengeAdmin"];

interface FormState {
  title: string;
  slug: string;
  category: string;
  difficulty: string;
  description: string;
  kind: string;
  flag: string;
  flag_case_insensitive: boolean;
  scoring: string;
  points_initial: string;
  points_min: string;
  decay: string;
  max_attempts: string;
  visible: boolean;
  image: string;
  internal_port: string;
  mem_limit_mb: string;
  cpu_millis: string;
  connection_template: string;
}

const empty: FormState = {
  title: "",
  slug: "",
  category: "misc",
  difficulty: "",
  description: "",
  kind: "standard",
  flag: "",
  flag_case_insensitive: false,
  scoring: "dynamic",
  points_initial: "500",
  points_min: "100",
  decay: "25",
  max_attempts: "",
  visible: false,
  image: "",
  internal_port: "",
  mem_limit_mb: "256",
  cpu_millis: "500",
  connection_template: "",
};

function fromChallenge(c: ChallengeAdmin): FormState {
  return {
    title: c.title,
    slug: c.slug,
    category: c.category,
    difficulty: c.difficulty ?? "",
    description: c.description,
    kind: c.kind,
    flag: c.flag,
    flag_case_insensitive: c.flag_case_insensitive,
    scoring: c.scoring,
    points_initial: String(c.points_initial),
    points_min: c.points_min != null ? String(c.points_min) : "100",
    decay: c.decay != null ? String(c.decay) : "25",
    max_attempts: c.max_attempts != null ? String(c.max_attempts) : "",
    visible: c.visible,
    image: c.image ?? "",
    internal_port: c.internal_port != null ? String(c.internal_port) : "",
    mem_limit_mb: String(c.mem_limit_mb),
    cpu_millis: String(c.cpu_millis),
    connection_template: c.connection_template ?? "",
  };
}

export function AdminChallengeEditor() {
  const { id } = useParams();
  const isNew = !id;
  const navigate = useNavigate();
  const { toast } = useToast();
  const { data: existing, isLoading } = useAdminChallenge(id ?? "", !isNew);
  const create = useCreateChallenge();
  const update = useUpdateChallenge(id ?? "");
  const del = useDeleteChallenge();
  const [form, setForm] = useState<FormState>(empty);
  const [err, setErr] = useState<RequestError | null>(null);

  useEffect(() => {
    if (existing) setForm(fromChallenge(existing));
  }, [existing]);

  if (!isNew && isLoading) return <Skeleton className="h-96 w-full" />;

  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => { setForm((f) => ({ ...f, [k]: v })); };
  const num = (s: string): number | undefined => (s === "" ? undefined : Number(s));

  const onSubmit = (e: SyntheticEvent) => {
    e.preventDefault();
    setErr(null);
    const base = {
      title: form.title,
      slug: form.slug || undefined,
      category: form.category as components["schemas"]["Category"],
      difficulty: form.difficulty ? (form.difficulty as components["schemas"]["Difficulty"]) : undefined,
      description: form.description,
      flag: form.flag,
      flag_case_insensitive: form.flag_case_insensitive,
      scoring: form.scoring as components["schemas"]["ScoringMode"],
      points_initial: Number(form.points_initial),
      points_min: form.scoring === "dynamic" ? num(form.points_min) : undefined,
      decay: form.scoring === "dynamic" ? num(form.decay) : undefined,
      max_attempts: num(form.max_attempts),
      visible: form.visible,
      mem_limit_mb: num(form.mem_limit_mb),
      cpu_millis: num(form.cpu_millis),
      connection_template: form.connection_template || undefined,
      ...(form.kind === "container"
        ? { image: form.image, internal_port: num(form.internal_port) }
        : {}),
    };

    if (isNew) {
      create.mutate(
        { ...base, kind: form.kind as components["schemas"]["ChallengeKind"] },
        {
          onSuccess: (c) => {
            toast({ title: "Challenge created", variant: "success" });
            void navigate(`/admin/challenges/${c.id}`);
          },
          onError: (e) => { setErr(e as RequestError); },
        },
      );
    } else {
      update.mutate(base, {
        onSuccess: () => { toast({ title: "Saved", variant: "success" }); },
        onError: (e) => { setErr(e as RequestError); },
      });
    }
  };

  const fe = (k: string) => err?.api.fields?.[k];

  return (
    <div>
      <AdminNav />
      <h1 className="mb-4 text-2xl font-bold text-text">
        {isNew ? "New challenge" : `Edit: ${form.title}`}
      </h1>
      <div className="grid gap-6 lg:grid-cols-3">
        <Card className="space-y-3 lg:col-span-2">
          <form onSubmit={onSubmit} className="space-y-3">
            <div className="grid gap-3 sm:grid-cols-2">
              <div>
                <Label>Title</Label>
                <Input value={form.title} onChange={(e) => { set("title", e.target.value); }} required />
                <FieldError messages={fe("title")} />
              </div>
              <div>
                <Label>Slug (optional)</Label>
                <Input value={form.slug} onChange={(e) => { set("slug", e.target.value); }} />
                <FieldError messages={fe("slug")} />
              </div>
              <div>
                <Label>Category</Label>
                <select
                  className="h-10 w-full rounded-md border border-border bg-surface px-3 text-sm text-text"
                  value={form.category}
                  onChange={(e) => { set("category", e.target.value); }}
                >
                  {["web", "pwn", "crypto", "rev", "forensics", "misc"].map((c) => (
                    <option key={c}>{c}</option>
                  ))}
                </select>
              </div>
              <div>
                <Label>Difficulty</Label>
                <select
                  className="h-10 w-full rounded-md border border-border bg-surface px-3 text-sm text-text"
                  value={form.difficulty}
                  onChange={(e) => { set("difficulty", e.target.value); }}
                >
                  <option value="">—</option>
                  {["intro", "easy", "medium", "hard", "insane"].map((d) => (
                    <option key={d}>{d}</option>
                  ))}
                </select>
              </div>
            </div>

            <div>
              <Label>Description (markdown)</Label>
              <textarea
                value={form.description}
                onChange={(e) => { set("description", e.target.value); }}
                rows={6}
                className="w-full rounded-md border border-border bg-surface p-3 font-mono text-sm text-text"
              />
            </div>

            <div>
              <Label>Flag</Label>
              <Input
                type="text"
                value={form.flag}
                onChange={(e) => { set("flag", e.target.value); }}
                className="font-mono"
                required
              />
              <FieldError messages={fe("flag")} />
              <label className="mt-1 flex items-center gap-2 text-sm text-text-muted">
                <input
                  type="checkbox"
                  checked={form.flag_case_insensitive}
                  onChange={(e) => { set("flag_case_insensitive", e.target.checked); }}
                />
                Case-insensitive
              </label>
            </div>

            <div className="grid gap-3 sm:grid-cols-4">
              <div>
                <Label>Scoring</Label>
                <select
                  className="h-10 w-full rounded-md border border-border bg-surface px-3 text-sm text-text"
                  value={form.scoring}
                  onChange={(e) => { set("scoring", e.target.value); }}
                >
                  <option value="dynamic">dynamic</option>
                  <option value="static">static</option>
                </select>
              </div>
              <div>
                <Label>Initial</Label>
                <Input
                  type="number"
                  value={form.points_initial}
                  onChange={(e) => { set("points_initial", e.target.value); }}
                />
                <FieldError messages={fe("points_initial")} />
              </div>
              {form.scoring === "dynamic" && (
                <>
                  <div>
                    <Label>Min</Label>
                    <Input
                      type="number"
                      value={form.points_min}
                      onChange={(e) => { set("points_min", e.target.value); }}
                    />
                    <FieldError messages={fe("points_min")} />
                  </div>
                  <div>
                    <Label>Decay</Label>
                    <Input
                      type="number"
                      value={form.decay}
                      onChange={(e) => { set("decay", e.target.value); }}
                    />
                    <FieldError messages={fe("decay")} />
                  </div>
                </>
              )}
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <div>
                <Label>Max attempts (blank = unlimited)</Label>
                <Input
                  type="number"
                  value={form.max_attempts}
                  onChange={(e) => { set("max_attempts", e.target.value); }}
                />
              </div>
              <div>
                <Label>Kind</Label>
                <select
                  className="h-10 w-full rounded-md border border-border bg-surface px-3 text-sm text-text disabled:opacity-50"
                  value={form.kind}
                  disabled={!isNew}
                  onChange={(e) => { set("kind", e.target.value); }}
                >
                  <option value="standard">standard</option>
                  <option value="container">container</option>
                </select>
              </div>
            </div>

            {form.kind === "container" && (
              <div className="grid gap-3 rounded-md border border-border p-3 sm:grid-cols-2">
                <div>
                  <Label>Image</Label>
                  <Input value={form.image} onChange={(e) => { set("image", e.target.value); }} className="font-mono" />
                  <FieldError messages={fe("image")} />
                </div>
                <div>
                  <Label>Internal port</Label>
                  <Input
                    type="number"
                    value={form.internal_port}
                    onChange={(e) => { set("internal_port", e.target.value); }}
                  />
                  <FieldError messages={fe("internal_port")} />
                </div>
                <div>
                  <Label>Memory (MB)</Label>
                  <Input
                    type="number"
                    value={form.mem_limit_mb}
                    onChange={(e) => { set("mem_limit_mb", e.target.value); }}
                  />
                </div>
                <div>
                  <Label>CPU (millis)</Label>
                  <Input
                    type="number"
                    value={form.cpu_millis}
                    onChange={(e) => { set("cpu_millis", e.target.value); }}
                  />
                </div>
                <div className="sm:col-span-2">
                  <Label>Connection template</Label>
                  <Input
                    value={form.connection_template}
                    onChange={(e) => { set("connection_template", e.target.value); }}
                    placeholder="nc {host} {port}"
                    className="font-mono"
                  />
                </div>
              </div>
            )}

            <label className="flex items-center gap-2 text-sm text-text">
              <input
                type="checkbox"
                checked={form.visible}
                onChange={(e) => { set("visible", e.target.checked); }}
              />
              Visible to participants
            </label>

            <div className="flex gap-2">
              <Button type="submit" disabled={create.isPending || update.isPending}>
                {isNew ? "Create" : "Save"}
              </Button>
              {!isNew && (
                <Button
                  type="button"
                  variant="danger"
                  onClick={() => {
                    if (!id) return;
                    if (confirm("Delete this challenge? This cannot be undone.")) {
                      del.mutate(
                        { id, confirm: true },
                        { onSuccess: () => { void navigate("/admin/challenges"); } },
                      );
                    }
                  }}
                >
                  Delete
                </Button>
              )}
            </div>
          </form>
        </Card>

        {!isNew && id && (
          <div className="space-y-4">
            {form.kind === "container" && <InstancePanel challengeId={id} />}
            {existing && <AttachmentsPanel challengeId={id} attachments={existing.attachments} />}
          </div>
        )}
      </div>
    </div>
  );
}
