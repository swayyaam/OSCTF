import type { components } from "../api/schema";
import { formatRelative } from "../lib/time";
import { CategoryBadge, EmptyState } from "./ui/misc";
import { Table, Td, Th } from "./ui/table";

type Solve = components["schemas"]["Solve"];

export function SolveList({ solves, showSolver = false }: { solves: Solve[]; showSolver?: boolean }) {
  if (solves.length === 0) {
    return <EmptyState>No solves yet.</EmptyState>;
  }
  return (
    <Table>
      <thead>
        <tr>
          <Th>Challenge</Th>
          <Th>Category</Th>
          {showSolver && <Th>Solver</Th>}
          <Th className="text-right">Points</Th>
          <Th className="text-right">When</Th>
        </tr>
      </thead>
      <tbody>
        {solves.map((s) => (
          <tr key={s.challenge_id}>
            <Td>{s.title}</Td>
            <Td>
              <CategoryBadge category={s.category} />
            </Td>
            {showSolver && <Td className="text-text-muted">{s.username ?? "—"}</Td>}
            <Td className="text-right font-mono text-primary">{s.points}</Td>
            <Td className="text-right text-text-muted" title={s.solved_at}>
              {formatRelative(s.solved_at)}
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
