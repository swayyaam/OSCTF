import { Link } from "react-router-dom";
import { useScoreboard } from "../api/hooks";
import { useLiveScoreboard } from "../ws/use-live";
import { formatRelative } from "../lib/time";
import { Table, Td, Th } from "../components/ui/table";
import { EmptyState, Skeleton } from "../components/ui/misc";
import { cn } from "../lib/cn";

export function ScoreboardPage() {
  const connected = useLiveScoreboard();
  const { data, isLoading } = useScoreboard();

  if (isLoading || !data) {
    return <Skeleton className="h-64 w-full" />;
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <h1 className="text-2xl font-bold text-text">Scoreboard</h1>
        {data.frozen && (
          <span className="rounded-full bg-warning/20 px-2 py-0.5 text-xs font-medium text-warning">
            Frozen
          </span>
        )}
        {!connected && (
          <span className="text-xs text-text-muted">reconnecting… (polling)</span>
        )}
      </div>

      {data.standings.length === 0 ? (
        <EmptyState>No teams yet.</EmptyState>
      ) : (
        <Table data-testid="scoreboard-table">
          <thead>
            <tr>
              <Th className="w-12">#</Th>
              <Th>Team</Th>
              <Th className="text-right">Points</Th>
              <Th className="text-right">Solves</Th>
              <Th className="text-right">Last solve</Th>
            </tr>
          </thead>
          <tbody aria-live="polite">
            {data.standings.map((row) => (
              <tr key={row.team_id} className={cn(row.banned && "opacity-50")}>
                <Td className="font-mono text-text-muted">{row.rank ?? "—"}</Td>
                <Td>
                  <Link
                    to={`/teams/${row.team_id}`}
                    className={cn("hover:text-primary", row.banned && "line-through")}
                  >
                    {row.name}
                  </Link>
                </Td>
                <Td className="text-right font-mono text-primary">{row.points}</Td>
                <Td className="text-right text-text-muted">{row.solves}</Td>
                <Td className="text-right text-text-muted">
                  {row.last_solve_at ? (
                    <span title={row.last_solve_at}>{formatRelative(row.last_solve_at)}</span>
                  ) : (
                    "—"
                  )}
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}
