import { useState } from "react";
import { Link } from "react-router-dom";
import { useAdminChallenges, useUpdateChallenge, type ChallengeFilters } from "../../api/admin-hooks";
import { AdminNav } from "./AdminNav";
import { Button } from "../../components/ui/button";
import { Table, Td, Th } from "../../components/ui/table";
import { Badge, CategoryBadge, Skeleton } from "../../components/ui/misc";

export function AdminChallengesPage() {
  const [filters, setFilters] = useState<ChallengeFilters>({ page: 1, per_page: 50 });
  const { data, isLoading } = useAdminChallenges(filters);

  return (
    <div>
      <AdminNav />
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-2xl font-bold text-text">Challenges</h1>
        <Link to="/admin/challenges/new">
          <Button>New challenge</Button>
        </Link>
      </div>

      <div className="mb-3 flex flex-wrap gap-2">
        <select
          className="rounded-md border border-border bg-surface px-2 py-1 text-sm text-text"
          value={filters.category ?? ""}
          onChange={(e) =>
            { setFilters({ ...filters, category: (e.target.value || undefined) as ChallengeFilters["category"] }); }
          }
        >
          <option value="">All categories</option>
          {["web", "pwn", "crypto", "rev", "forensics", "misc"].map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
        <select
          className="rounded-md border border-border bg-surface px-2 py-1 text-sm text-text"
          value={filters.visible === undefined ? "" : String(filters.visible)}
          onChange={(e) =>
            { setFilters({
              ...filters,
              visible: e.target.value === "" ? undefined : e.target.value === "true",
            }); }
          }
        >
          <option value="">Any visibility</option>
          <option value="true">Visible</option>
          <option value="false">Hidden</option>
        </select>
      </div>

      {isLoading || !data ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Title</Th>
              <Th>Category</Th>
              <Th>Kind</Th>
              <Th className="text-right">Points</Th>
              <Th className="text-right">Solves</Th>
              <Th>Visible</Th>
            </tr>
          </thead>
          <tbody>
            {data.items.map((c) => (
              <ChallengeRow key={c.id} challenge={c} />
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}

type Challenge = import("../../api/schema").components["schemas"]["ChallengeAdmin"];

function ChallengeRow({ challenge }: { challenge: Challenge }) {
  const update = useUpdateChallenge(challenge.id);
  return (
    <tr>
      <Td>
        <Link to={`/admin/challenges/${challenge.id}`} className="text-primary">
          {challenge.title}
        </Link>
      </Td>
      <Td>
        <CategoryBadge category={challenge.category} />
      </Td>
      <Td>{challenge.kind === "container" ? <Badge>container</Badge> : "standard"}</Td>
      <Td className="text-right font-mono">{challenge.current_points}</Td>
      <Td className="text-right text-text-muted">{challenge.solves}</Td>
      <Td>
        <button
          onClick={() => { update.mutate({ visible: !challenge.visible }); }}
          className={challenge.visible ? "text-success" : "text-text-muted"}
        >
          {challenge.visible ? "Visible" : "Hidden"}
        </button>
      </Td>
    </tr>
  );
}
