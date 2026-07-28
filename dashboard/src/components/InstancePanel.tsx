import { useStartInstance, useStopInstance, useExtendInstance } from "../api/hooks";
import { RequestError } from "../api/client";
import { Button } from "./ui/button";
import { useCountdown } from "../lib/time";
import { cn } from "../lib/cn";
import type { components } from "../api/schema";

type TeamInstance = components["schemas"]["TeamInstance"];

interface Props {
  slug: string;
  instance: TeamInstance | null | undefined;
}

/** InstancePanel renders the participant Start/Stop/Extend controls, connection
 * info, and a TTL countdown for a per_team challenge. */
export function InstancePanel({ slug, instance }: Props) {
  const start = useStartInstance(slug);
  const stop = useStopInstance(slug);
  const extend = useExtendInstance(slug);

  const state = instance?.state;
  const running = state === "running";
  const pending = state === "pending" || state === "starting";
  const errored = state === "error";
  const busy = start.isPending || stop.isPending || extend.isPending;

  const quotaError =
    start.error instanceof RequestError && start.error.api.status === 409
      ? start.error.api.detail ?? start.error.api.title
      : null;

  return (
    <div className="rounded-md border border-border bg-surface-2 p-3" data-testid="instance-panel">
      <div className="mb-2 flex items-center justify-between">
        <span className="text-xs uppercase text-text-muted">Your instance</span>
        {state && (
          <span data-testid="instance-state" className="font-mono text-xs text-text-muted">
            {state}
          </span>
        )}
      </div>

      {(!instance || (!running && !pending && !errored)) && (
        <Button data-testid="instance-start" onClick={() => { start.mutate(); }} disabled={busy}>
          {start.isPending ? "Starting…" : "Start instance"}
        </Button>
      )}

      {pending && (
        <p className="text-sm text-text-muted" data-testid="instance-pending">
          Starting your instance… this can take a moment.
        </p>
      )}

      {errored && (
        <div className="space-y-2">
          <p className="text-sm text-danger">{instance?.error ?? "The instance failed to start."}</p>
          <Button data-testid="instance-start" onClick={() => { start.mutate(); }} disabled={busy}>
            Retry
          </Button>
        </div>
      )}

      {running && instance && (
        <div className="space-y-3">
          {instance.connection_info && (
            <div className="flex items-center gap-2">
              <code className="flex-1 font-mono text-sm text-text">{instance.connection_info}</code>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => void navigator.clipboard.writeText(instance.connection_info ?? "")}
              >
                Copy
              </Button>
            </div>
          )}
          <div className="flex items-center gap-3">
            <ExpiryCountdown expiresAt={instance.expires_at ?? undefined} />
            {instance.expires_at && (
              <Button
                size="sm"
                variant="secondary"
                data-testid="instance-extend"
                onClick={() => { extend.mutate(); }}
                disabled={busy}
              >
                Extend
              </Button>
            )}
            <Button
              size="sm"
              variant="danger"
              data-testid="instance-stop"
              onClick={() => { stop.mutate(); }}
              disabled={busy}
            >
              Stop
            </Button>
          </div>
        </div>
      )}

      {quotaError && <p className="mt-2 text-sm text-warning">{quotaError}</p>}
    </div>
  );
}

function ExpiryCountdown({ expiresAt }: { expiresAt: string | undefined }) {
  const c = useCountdown(expiresAt);
  if (!expiresAt) return <span className="text-sm text-text-muted">No expiry</span>;
  if (c.done) return <span className="text-sm text-danger">expired — start again</span>;
  const total = c.days * 24 + c.hours;
  const label =
    c.days > 0
      ? `${String(c.days)}d ${String(c.hours)}h`
      : `${String(total)}:${String(c.minutes).padStart(2, "0")}:${String(c.seconds).padStart(2, "0")}`;
  const low = c.days === 0 && c.hours === 0 && c.minutes < 5;
  return (
    <span
      data-testid="instance-countdown"
      className={cn("font-mono text-sm", low ? "text-warning" : "text-text-muted")}
    >
      expires in {label}
    </span>
  );
}
