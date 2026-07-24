import { Link } from "react-router-dom";
import { useEvent, useMe } from "../api/hooks";
import { useCountdown } from "../lib/time";
import { Markdown } from "../lib/markdown";
import { Button } from "../components/ui/button";
import { Skeleton } from "../components/ui/misc";

export function LandingPage() {
  const { data: event, isLoading } = useEvent();
  const { data: me } = useMe();

  if (isLoading || !event) {
    return (
      <div className="mx-auto max-w-2xl space-y-4 py-12">
        <Skeleton className="h-10 w-2/3" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  const target = event.phase === "pre" ? event.starts_at : event.ends_at;

  return (
    <div className="mx-auto max-w-2xl space-y-8 py-12 text-center">
      <div>
        <h1 className="text-4xl font-bold text-text">{event.name}</h1>
        <div className="mt-4 text-left">
          <Markdown>{event.description || "_No description yet._"}</Markdown>
        </div>
      </div>

      <PhaseBanner phase={event.phase} target={target} />

      <div className="flex justify-center gap-3">
        {me ? (
          <Link to="/challenges">
            <Button>Go to challenges</Button>
          </Link>
        ) : (
          <>
            <Link to="/register">
              <Button>Register</Button>
            </Link>
            <Link to="/login">
              <Button variant="secondary">Log in</Button>
            </Link>
          </>
        )}
        <Link to="/scoreboard">
          <Button variant="ghost">Scoreboard</Button>
        </Link>
      </div>
    </div>
  );
}

function PhaseBanner({ phase, target }: { phase: string; target: string }) {
  const cd = useCountdown(phase === "ended" ? undefined : target);
  if (phase === "ended") {
    return <p className="text-lg text-text-muted">This event has ended.</p>;
  }
  const label = phase === "pre" ? "Starts in" : "Ends in";
  return (
    <div>
      <p className="text-sm uppercase tracking-wide text-text-muted">{label}</p>
      <p className="mt-1 font-mono text-3xl text-primary" data-testid="countdown">
        {cd.days}d {String(cd.hours).padStart(2, "0")}:{String(cd.minutes).padStart(2, "0")}:
        {String(cd.seconds).padStart(2, "0")}
      </p>
    </div>
  );
}
