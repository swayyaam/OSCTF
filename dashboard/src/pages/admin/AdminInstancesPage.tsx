import { useState } from "react";
import { useAdminInstances, useAdminDestroyInstanceById } from "../../api/admin-hooks";
import { AdminNav } from "./AdminNav";
import { formatRelative } from "../../lib/time";
import { Table, Td, Th } from "../../components/ui/table";
import { Button } from "../../components/ui/button";
import { Skeleton, EmptyState } from "../../components/ui/misc";
import { cn } from "../../lib/cn";

const stateColor: Record<string, string> = {
  running: "text-success",
  starting: "text-warning",
  pending: "text-warning",
  unhealthy: "text-warning",
  stopped: "text-text-muted",
  error: "text-danger",
  lost: "text-danger",
};

type Owner = "all" | "shared" | "per_team";

export function AdminInstancesPage() {
  const { data, isLoading } = useAdminInstances();
  const destroy = useAdminDestroyInstanceById();
  const [owner, setOwner] = useState<Owner>("all");

  const items = (data?.items ?? []).filter((i) => {
    if (owner === "shared") return i.team_id == null;
    if (owner === "per_team") return i.team_id != null;
    return true;
  });

  return (
    <div>
      <AdminNav />
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-2xl font-bold text-text">Instances</h1>
        <select
          className="rounded-md border border-border bg-surface px-2 py-1 text-sm text-text"
          value={owner}
          onChange={(e) => { setOwner(e.target.value as Owner); }}
        >
          <option value="all">All owners</option>
          <option value="shared">Shared</option>
          <option value="per_team">Per-team</option>
        </select>
      </div>

      {isLoading || !data ? (
        <Skeleton className="h-64 w-full" />
      ) : items.length === 0 ? (
        <EmptyState>No instances running.</EmptyState>
      ) : (
        <Table data-testid="admin-instances-table">
          <thead>
            <tr>
              <Th>Challenge</Th>
              <Th>Owner</Th>
              <Th>State</Th>
              <Th className="text-right">Port</Th>
              <Th>Network</Th>
              <Th className="text-right">Age</Th>
              <Th className="text-right">Expiry</Th>
              <Th>Health</Th>
              <Th></Th>
            </tr>
          </thead>
          <tbody>
            {items.map((i) => (
              <tr key={i.id}>
                <Td className="font-mono text-xs">{i.challenge_slug ?? i.challenge_id.slice(0, 8)}</Td>
                <Td>
                  {i.team_id == null ? (
                    <span className="rounded-full bg-surface-2 px-2 py-0.5 text-xs text-text-muted">shared</span>
                  ) : (
                    (i.team_name ?? i.team_id.slice(0, 8))
                  )}
                </Td>
                <Td className={cn("font-mono text-xs", stateColor[i.state] ?? "text-text")}>{i.state}</Td>
                <Td className="text-right font-mono text-text-muted">{i.host_port ?? "—"}</Td>
                <Td className="font-mono text-xs text-text-muted">{i.network ?? "—"}</Td>
                <Td className="text-right text-text-muted">
                  {i.started_at ? formatRelative(i.started_at) : "—"}
                </Td>
                <Td className="text-right text-text-muted">
                  {i.expires_at ? formatRelative(i.expires_at) : "—"}
                </Td>
                <Td className="text-text-muted">
                  {i.last_health_at ? formatRelative(i.last_health_at) : "—"}
                </Td>
                <Td className="text-right">
                  <Button
                    size="sm"
                    variant="danger"
                    data-testid="admin-instance-destroy"
                    disabled={destroy.isPending}
                    onClick={() => { destroy.mutate(i.id); }}
                  >
                    Destroy
                  </Button>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {data && <p className="mt-2 text-xs text-text-muted">{items.length} instances</p>}
    </div>
  );
}
