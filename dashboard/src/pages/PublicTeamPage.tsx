import { Link, useParams } from "react-router-dom";
import { usePublicTeam } from "../api/hooks";
import { SolveList } from "../components/SolveList";
import { Card, Skeleton } from "../components/ui/misc";

export function PublicTeamPage() {
  const { id = "" } = useParams();
  const { data: team, isLoading, error } = usePublicTeam(id);

  if (isLoading) return <Skeleton className="h-40 w-full" />;
  if (error || !team) return <p className="text-text-muted">Team not found.</p>;

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-text">{team.name}</h1>
        <div className="text-right">
          <div className="font-mono text-xl text-primary">{team.points} pts</div>
          {team.rank != null && <div className="text-sm text-text-muted">Rank #{team.rank}</div>}
        </div>
      </div>
      <Card>
        <h2 className="mb-2 font-semibold text-text">Members</h2>
        <ul className="space-y-1 text-sm">
          {team.members.map((m) => (
            <li key={m.id}>
              <Link to={`/users/${m.id}`} className="text-text-muted hover:text-primary">
                {m.username}
              </Link>
            </li>
          ))}
        </ul>
      </Card>
      <SolveList solves={team.solves} showSolver />
    </div>
  );
}
