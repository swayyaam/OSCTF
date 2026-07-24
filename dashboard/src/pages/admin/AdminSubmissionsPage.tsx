import { useState } from "react";
import { useAdminSubmissions, type SubmissionFilters } from "../../api/admin-hooks";
import { AdminNav } from "./AdminNav";
import { formatLocal } from "../../lib/time";
import { Table, Td, Th } from "../../components/ui/table";
import { Skeleton } from "../../components/ui/misc";

export function AdminSubmissionsPage() {
  const [filters, setFilters] = useState<SubmissionFilters>({ page: 1, per_page: 50 });
  const [autoRefresh, setAutoRefresh] = useState(false);
  const { data, isLoading } = useAdminSubmissions(filters, autoRefresh);

  return (
    <div>
      <AdminNav />
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-2xl font-bold text-text">Submissions</h1>
        <label className="flex items-center gap-2 text-sm text-text-muted">
          <input type="checkbox" checked={autoRefresh} onChange={(e) => { setAutoRefresh(e.target.checked); }} />
          Auto-refresh (10s)
        </label>
      </div>

      <div className="mb-3 flex flex-wrap gap-2">
        <select
          className="rounded-md border border-border bg-surface px-2 py-1 text-sm text-text"
          value={filters.correct === undefined ? "" : String(filters.correct)}
          onChange={(e) =>
            { setFilters({
              ...filters,
              correct: e.target.value === "" ? undefined : e.target.value === "true",
              page: 1,
            }); }
          }
        >
          <option value="">All</option>
          <option value="true">Correct</option>
          <option value="false">Incorrect</option>
        </select>
      </div>

      {isLoading || !data ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Time</Th>
              <Th>Team</Th>
              <Th>User</Th>
              <Th>Challenge</Th>
              <Th>Flag</Th>
              <Th>IP</Th>
              <Th>✓</Th>
            </tr>
          </thead>
          <tbody>
            {data.items.map((s) => (
              <tr key={s.id}>
                <Td className="whitespace-nowrap text-text-muted">{formatLocal(s.created_at)}</Td>
                <Td>{s.team.name}</Td>
                <Td className="text-text-muted">{s.user.username}</Td>
                <Td>{s.challenge.title}</Td>
                <Td className="font-mono text-xs">{s.provided}</Td>
                <Td className="text-text-muted">{s.ip ?? "—"}</Td>
                <Td className={s.correct ? "text-success" : "text-text-muted"}>
                  {s.correct ? "✓" : "✗"}
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {data && <p className="mt-2 text-xs text-text-muted">{data.total} submissions</p>}
    </div>
  );
}
