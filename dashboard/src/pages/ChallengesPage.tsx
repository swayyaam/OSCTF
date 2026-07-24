import { useNavigate, useParams } from "react-router-dom";
import { useChallenges } from "../api/hooks";
import { useLiveScoreboard } from "../ws/use-live";
import { RequestError } from "../api/client";
import type { components } from "../api/schema";
import { CategoryBadge, Card, EmptyState, Skeleton } from "../components/ui/misc";
import { ChallengeDialog } from "../components/ChallengeDialog";
import { cn } from "../lib/cn";

type Challenge = components["schemas"]["Challenge"];

const CATEGORY_ORDER = ["web", "pwn", "crypto", "rev", "forensics", "misc"];

export function ChallengesPage() {
  useLiveScoreboard();
  const { slug } = useParams();
  const navigate = useNavigate();
  const { data: challenges, isLoading, error } = useChallenges();

  if (error instanceof RequestError && error.api.status === 403) {
    return (
      <EmptyState>The challenge board opens when the event starts. Check back at start time.</EmptyState>
    );
  }
  if (isLoading || !challenges) {
    return (
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-28" />
        ))}
      </div>
    );
  }
  if (challenges.length === 0) {
    return <EmptyState>No challenges yet — check back soon.</EmptyState>;
  }

  const byCategory = new Map<string, Challenge[]>();
  for (const c of challenges) {
    const list = byCategory.get(c.category) ?? [];
    list.push(c);
    byCategory.set(c.category, list);
  }
  const categories = [...byCategory.keys()].sort(
    (a, b) => CATEGORY_ORDER.indexOf(a) - CATEGORY_ORDER.indexOf(b),
  );

  return (
    <div className="space-y-8">
      {categories.map((cat) => (
        <section key={cat}>
          <h2 className="mb-3 flex items-center gap-2 text-lg font-semibold text-text">
            <CategoryBadge category={cat} />
          </h2>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {(byCategory.get(cat) ?? [])
              .sort((a, b) => b.points - a.points)
              .map((c) => (
                <button
                  key={c.id}
                  onClick={() => { void navigate(`/challenges/${c.slug}`); }}
                  className="text-left"
                >
                  <Card
                    className={cn(
                      "h-full transition hover:border-primary",
                      c.solved_by_me && "border-success/60",
                    )}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <h3 className="font-semibold text-text">{c.title}</h3>
                      {c.solved_by_me && <span className="text-success">✓</span>}
                    </div>
                    <div className="mt-2 flex items-center justify-between text-sm text-text-muted">
                      <span className="font-mono text-primary">{c.points} pts</span>
                      <span>
                        {c.solves} solve{c.solves === 1 ? "" : "s"}
                      </span>
                    </div>
                    {c.difficulty && (
                      <div className="mt-2 text-xs text-text-muted">{c.difficulty}</div>
                    )}
                  </Card>
                </button>
              ))}
          </div>
        </section>
      ))}

      {slug && (
        <ChallengeDialog
          slug={slug}
          onClose={() => { void navigate("/challenges"); }}
        />
      )}
    </div>
  );
}
