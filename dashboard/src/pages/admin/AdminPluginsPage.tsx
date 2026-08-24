import { useState } from "react";
import { useAdminPlugins, useReloadPlugin } from "../../api/admin-hooks";
import { AdminNav } from "./AdminNav";
import { Table, Td, Th } from "../../components/ui/table";
import { Skeleton } from "../../components/ui/misc";
import { cn } from "../../lib/cn";
import type { RequestError } from "../../api/client";

/**
 * States a plugin can be in, mapped to how alarming they are. `ready` is the only good one;
 * `failed` means an operator has to do something, and the transient ones are just noise if
 * they are shown as problems.
 */
function stateTone(state: string): string {
  switch (state) {
    case "ready":
      return "text-success";
    case "failed":
    case "quarantined":
      return "text-danger";
    default:
      return "text-warning";
  }
}

export function AdminPluginsPage() {
  const { data, isLoading } = useAdminPlugins();
  const reload = useReloadPlugin();
  const [busy, setBusy] = useState<string | null>(null);
  const [note, setNote] = useState<{ name: string; ok: boolean; text: string } | null>(null);

  const doReload = (name: string) => {
    setBusy(name);
    setNote(null);
    reload.mutate(name, {
      onSuccess: () => {
        setNote({ name, ok: true, text: "Reloaded; the new instance is serving." });
      },
      onError: (e: unknown) => {
        const detail = (e as RequestError).api.detail ?? "The reload failed.";
        // Worth saying explicitly: the deployment is unchanged, not broken.
        setNote({ name, ok: false, text: `${detail} The previous instance is still serving.` });
      },
      onSettled: () => {
        setBusy(null);
      },
    });
  };

  return (
    <div>
      <AdminNav />
      <h1 className="mb-1 text-2xl font-bold text-text">Plugins</h1>
      <p className="mb-4 text-sm text-text-muted">
        Every plugin the platform tracks, including ones quarantined at load — so you can see why a
        plugin is not working without reading boot logs. Reloading launches a new instance and swaps
        to it once ready; if it never becomes ready the old one keeps serving.
      </p>

      {note && (
        <div
          className={cn(
            "mb-4 rounded-md border px-3 py-2 text-sm",
            note.ok ? "border-success text-success" : "border-danger text-danger",
          )}
        >
          <span className="font-medium">{note.name}:</span> {note.text}
        </div>
      )}

      {isLoading || !data ? (
        <Skeleton className="h-48 w-full" />
      ) : data.plugins.length === 0 ? (
        <p className="rounded-md border border-border px-3 py-6 text-center text-sm text-text-muted">
          No plugins are installed. Drop a plugin directory into the plugins directory and restart.
        </p>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th>Type</Th>
              <Th>State</Th>
              <Th>Reason</Th>
              <Th>Actions</Th>
            </tr>
          </thead>
          <tbody>
            {data.plugins.map((p) => (
              <tr key={p.name}>
                <Td className="font-medium text-text">{p.name}</Td>
                <Td>{p.type}</Td>
                <Td className={stateTone(p.state)}>{p.state}</Td>
                <Td className="max-w-md text-text-muted">{p.reason ?? "—"}</Td>
                <Td>
                  <button
                    type="button"
                    disabled={busy !== null}
                    onClick={() => {
                      doReload(p.name);
                    }}
                    className="rounded-md border border-border px-2 py-1 text-sm text-text hover:bg-surface-2 disabled:opacity-50"
                  >
                    {busy === p.name ? "Reloading…" : "Reload"}
                  </button>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}
