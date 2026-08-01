import { useEffect, useState, type SyntheticEvent } from "react";
import { useAdminEvent, useUpdateEvent } from "../../api/admin-hooks";
import { RequestError } from "../../api/client";
import { useToast } from "../../components/ui/toast";
import { AdminNav } from "./AdminNav";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { Card, FieldError, Label, Skeleton } from "../../components/ui/misc";

// Convert an ISO string to the value a datetime-local input expects (UTC-explicit).
function toLocalInput(iso: string | null | undefined): string {
  if (!iso) return "";
  return new Date(iso).toISOString().slice(0, 16);
}
function fromLocalInput(v: string): string | null {
  if (!v) return null;
  return new Date(v + "Z").toISOString();
}

export function AdminEventPage() {
  const { data: event, isLoading } = useAdminEvent();
  const update = useUpdateEvent();
  const { toast } = useToast();
  const [form, setForm] = useState({ name: "", description: "", starts_at: "", ends_at: "", freeze_at: "" });
  const [err, setErr] = useState<RequestError | null>(null);

  useEffect(() => {
    if (event) {
      setForm({
        name: event.name,
        description: event.description,
        starts_at: toLocalInput(event.starts_at),
        ends_at: toLocalInput(event.ends_at),
        freeze_at: toLocalInput(event.freeze_at),
      });
    }
  }, [event]);

  if (isLoading || !event) return <Skeleton className="h-64 w-full" />;

  // A freeze persists until an admin clears it — it is NOT lifted when the event ends.
  // Surface the footgun: a frozen scoreboard that outlives the event hides final
  // standings from everyone but admins indefinitely (3a-viii).
  const eventEnded = !!event.ends_at && new Date(event.ends_at).getTime() < Date.now();
  const freezeStillSet = !!event.freeze_at;

  const onSubmit = (e: SyntheticEvent) => {
    e.preventDefault();
    setErr(null);
    update.mutate(
      {
        name: form.name,
        description: form.description,
        starts_at: fromLocalInput(form.starts_at) ?? undefined,
        ends_at: fromLocalInput(form.ends_at) ?? undefined,
        freeze_at: fromLocalInput(form.freeze_at),
      },
      {
        onSuccess: () => { toast({ title: "Event updated", variant: "success" }); },
        onError: (e) => { setErr(e as RequestError); },
      },
    );
  };

  return (
    <div>
      <AdminNav />
      <h1 className="mb-4 text-2xl font-bold text-text">Event settings</h1>
      {eventEnded && freezeStillSet && (
        <div
          role="alert"
          className="mb-4 max-w-xl rounded-md border border-warning/40 bg-warning/20 p-3 text-sm text-warning"
        >
          <span className="font-medium">Scoreboard still frozen after the event ended.</span>{" "}
          The freeze is not lifted automatically — the public scoreboard stays frozen and final
          standings remain hidden from everyone but admins until you clear the freeze time below.
        </div>
      )}
      <Card className="max-w-xl">
        <p className="mb-3 text-xs text-text-muted">All times are UTC.</p>
        <form onSubmit={onSubmit} className="space-y-3">
          <div>
            <Label htmlFor="name">Name</Label>
            <Input id="name" value={form.name} onChange={(e) => { setForm({ ...form, name: e.target.value }); }} />
            <FieldError messages={err?.api.fields?.name} />
          </div>
          <div>
            <Label htmlFor="desc">Description (markdown)</Label>
            <textarea
              id="desc"
              value={form.description}
              onChange={(e) => { setForm({ ...form, description: e.target.value }); }}
              rows={5}
              className="w-full rounded-md border border-border bg-surface p-3 font-mono text-sm text-text"
            />
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div>
              <Label htmlFor="start">Starts (UTC)</Label>
              <Input
                id="start"
                type="datetime-local"
                value={form.starts_at}
                onChange={(e) => { setForm({ ...form, starts_at: e.target.value }); }}
              />
            </div>
            <div>
              <Label htmlFor="end">Ends (UTC)</Label>
              <Input
                id="end"
                type="datetime-local"
                value={form.ends_at}
                onChange={(e) => { setForm({ ...form, ends_at: e.target.value }); }}
              />
              <FieldError messages={err?.api.fields?.ends_at} />
            </div>
            <div>
              <Label htmlFor="freeze">Freeze (UTC, optional)</Label>
              <Input
                id="freeze"
                type="datetime-local"
                value={form.freeze_at}
                onChange={(e) => { setForm({ ...form, freeze_at: e.target.value }); }}
              />
              <FieldError messages={err?.api.fields?.freeze_at} />
            </div>
          </div>
          <Button type="submit" disabled={update.isPending}>
            Save
          </Button>
        </form>
      </Card>
    </div>
  );
}
