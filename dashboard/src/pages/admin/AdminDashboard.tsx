import { Link } from "react-router-dom";
import { useAdminStats } from "../../api/admin-hooks";
import { AdminNav } from "./AdminNav";
import { Card, Skeleton } from "../../components/ui/misc";

const tiles: { key: keyof Stats; label: string }[] = [
  { key: "users", label: "Users" },
  { key: "teams", label: "Teams" },
  { key: "submissions", label: "Submissions" },
  { key: "solves", label: "Solves" },
  { key: "instances_running", label: "Instances running" },
  { key: "ws_connections", label: "Live connections" },
];

interface Stats {
  users: number;
  teams: number;
  submissions: number;
  solves: number;
  instances_running: number;
  ws_connections: number;
}

export function AdminDashboard() {
  const { data: stats, isLoading } = useAdminStats();

  return (
    <div>
      <AdminNav />
      <h1 className="mb-4 text-2xl font-bold text-text">Admin dashboard</h1>
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-3">
        {tiles.map((t) => (
          <Card key={t.key}>
            <div className="text-sm text-text-muted">{t.label}</div>
            {isLoading || !stats ? (
              <Skeleton className="mt-1 h-8 w-16" />
            ) : (
              <div className="mt-1 font-mono text-3xl text-primary">{stats[t.key]}</div>
            )}
          </Card>
        ))}
      </div>
      <div className="mt-6 flex flex-wrap gap-3 text-sm">
        <Link to="/admin/challenges/new" className="text-primary">
          + New challenge
        </Link>
        <Link to="/admin/event" className="text-primary">
          Edit event
        </Link>
      </div>
    </div>
  );
}
