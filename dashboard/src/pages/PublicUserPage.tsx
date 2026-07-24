import { Link, useParams } from "react-router-dom";
import { usePublicUser } from "../api/hooks";
import { SolveList } from "../components/SolveList";
import { Skeleton } from "../components/ui/misc";

export function PublicUserPage() {
  const { id = "" } = useParams();
  const { data: user, isLoading, error } = usePublicUser(id);

  if (isLoading) return <Skeleton className="h-40 w-full" />;
  if (error || !user) return <p className="text-text-muted">User not found.</p>;

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <div>
        <h1 className="text-2xl font-bold text-text">{user.username}</h1>
        {user.team && (
          <Link to={`/teams/${user.team.id}`} className="text-sm text-primary">
            {user.team.name}
          </Link>
        )}
      </div>
      <SolveList solves={user.solves} />
    </div>
  );
}
