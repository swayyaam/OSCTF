import { useState } from "react";
import { Link } from "react-router-dom";
import { useAdminTeams, useUpdateTeam } from "../../api/admin-hooks";
import type { components } from "../../api/schema";
import { AdminNav } from "./AdminNav";
import { Input } from "../../components/ui/input";
import { Table, Td, Th } from "../../components/ui/table";
import { Skeleton } from "../../components/ui/misc";

type TeamAdmin = components["schemas"]["TeamAdmin"];

export function AdminTeamsPage() {
  const [filters, setFilters] = useState<{ page?: number; per_page?: number; q?: string }>({
    page: 1,
    per_page: 50,
  });
  const { data, isLoading } = useAdminTeams(filters);

  return (
    <div>
      <AdminNav />
      <h1 className="mb-4 text-2xl font-bold text-text">Teams</h1>
      <Input
        placeholder="Search team name…"
        className="mb-3 max-w-sm"
        onChange={(e) => { setFilters({ ...filters, q: e.target.value || undefined, page: 1 }); }}
      />
      {isLoading || !data ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th className="text-right">Members</Th>
              <Th>Flags</Th>
            </tr>
          </thead>
          <tbody>
            {data.items.map((t) => (
              <TeamRow key={t.id} team={t} />
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}

function TeamRow({ team }: { team: TeamAdmin }) {
  const update = useUpdateTeam();
  return (
    <tr>
      <Td>
        <Link to={`/teams/${team.id}`} className="text-primary">
          {team.name}
        </Link>
      </Td>
      <Td className="text-right text-text-muted">{team.member_count}</Td>
      <Td className="space-x-2 text-xs">
        <button
          className={team.banned ? "text-danger" : "text-text-muted"}
          onClick={() => { update.mutate({ id: team.id, body: { banned: !team.banned } }); }}
        >
          {team.banned ? "banned" : "ban"}
        </button>
        <button
          className={team.hidden ? "text-warning" : "text-text-muted"}
          onClick={() => { update.mutate({ id: team.id, body: { hidden: !team.hidden } }); }}
        >
          {team.hidden ? "hidden" : "hide"}
        </button>
      </Td>
    </tr>
  );
}
