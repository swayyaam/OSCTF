import { useState } from "react";
import {
  fetchInstanceLogs,
  useDeployInstance,
  useDestroyInstance,
  useInstance,
  useRestartInstance,
} from "../../api/admin-hooks";
import { RequestError } from "../../api/client";
import { useToast } from "../../components/ui/toast";
import { Button } from "../../components/ui/button";
import { Badge, Card } from "../../components/ui/misc";

const stateColor: Record<string, string> = {
  running: "text-success",
  starting: "text-warning",
  pending: "text-warning",
  unhealthy: "text-warning",
  stopped: "text-text-muted",
  error: "text-danger",
  lost: "text-danger",
};

export function InstancePanel({ challengeId }: { challengeId: string }) {
  const { data: instance } = useInstance(challengeId, true);
  const deploy = useDeployInstance(challengeId);
  const restart = useRestartInstance(challengeId);
  const destroy = useDestroyInstance(challengeId);
  const { toast } = useToast();
  const [logs, setLogs] = useState<string | null>(null);

  const busy = deploy.isPending || restart.isPending || destroy.isPending;
  const onError = (e: unknown) =>
    { toast({ title: (e as RequestError).api.detail ?? "Runtime error", variant: "danger" }); };

  const loadLogs = () => {
    fetchInstanceLogs(challengeId, 200)
      .then(setLogs)
      .catch((e: unknown) => { onError(e); });
  };

  return (
    <Card className="space-y-3">
      <h2 className="font-semibold text-text">Instance</h2>
      <div className="flex items-center gap-2 text-sm">
        <span className="text-text-muted">State:</span>
        {instance ? (
          <span className={stateColor[instance.state] ?? "text-text"}>{instance.state}</span>
        ) : (
          <span className="text-text-muted">none</span>
        )}
        {instance?.host_port != null && <Badge>port {instance.host_port}</Badge>}
      </div>

      {instance?.connection_info && (
        <code className="block rounded bg-surface-2 p-2 font-mono text-xs text-text">
          {instance.connection_info}
        </code>
      )}
      {instance?.error && <p className="text-xs text-danger">{instance.error}</p>}

      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          data-testid="instance-deploy"
          disabled={busy}
          onClick={() =>
            { deploy.mutate(undefined, {
              onSuccess: () => { toast({ title: "Instance deployed", variant: "success" }); },
              onError,
            }); }
          }
        >
          {instance && instance.state === "running" ? "Redeploy" : "Deploy"}
        </Button>
        {instance && (
          <>
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => { restart.mutate(undefined, { onError }); }}>
              Restart
            </Button>
            <Button
              size="sm"
              variant="danger"
              disabled={busy}
              onClick={() => { destroy.mutate(undefined, { onError }); }}
            >
              Destroy
            </Button>
          </>
        )}
        <Button size="sm" variant="ghost" onClick={loadLogs}>
          Logs
        </Button>
      </div>

      {logs != null && (
        <pre className="max-h-64 overflow-auto rounded bg-black/60 p-2 font-mono text-xs text-text">
          {logs || "(no output)"}
        </pre>
      )}
    </Card>
  );
}
