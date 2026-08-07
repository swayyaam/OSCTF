import { useState, type SyntheticEvent } from "react";
import { useChallenge, useSubmitFlag } from "../api/hooks";
import { API_BASE, RequestError } from "../api/client";
import { Markdown } from "../lib/markdown";
import { Dialog } from "./ui/dialog";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { CategoryBadge, Skeleton } from "./ui/misc";
import { InstancePanel } from "./InstancePanel";
import { cn } from "../lib/cn";

interface Props {
  slug: string;
  onClose: () => void;
}

type Feedback =
  | { kind: "none" }
  | { kind: "correct"; points: number | null }
  | { kind: "wrong" }
  | { kind: "cooldown"; seconds: number }
  | { kind: "error"; message: string };

export function ChallengeDialog({ slug, onClose }: Props) {
  const { data: chal, isLoading } = useChallenge(slug);
  const submit = useSubmitFlag(slug);
  const [flag, setFlag] = useState("");
  const [feedback, setFeedback] = useState<Feedback>({ kind: "none" });

  const onSubmit = (e: SyntheticEvent) => {
    e.preventDefault();
    setFeedback({ kind: "none" });
    submit.mutate(flag, {
      onSuccess: (res) => {
        if (res.correct) {
          setFeedback({ kind: "correct", points: res.points ?? null });
          setFlag("");
        } else {
          setFeedback({ kind: "wrong" });
        }
      },
      onError: (e) => {
        const err = e as RequestError;
        if (err.api.status === 429) {
          setFeedback({ kind: "cooldown", seconds: 60 });
        } else if (err.api.status === 409) {
          setFeedback({ kind: "error", message: "Your team has already solved this." });
        } else {
          setFeedback({ kind: "error", message: err.api.detail ?? err.api.title });
        }
      },
    });
  };

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose(); }} title={chal?.title ?? "Challenge"}>
      {isLoading || !chal ? (
        <div className="space-y-3">
          <Skeleton className="h-6 w-40" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-2 text-sm text-text-muted">
            <CategoryBadge category={chal.category} />
            <span className="font-mono text-primary">{chal.points} pts</span>
            <span>· {chal.solves} solves</span>
            {chal.difficulty && <span>· {chal.difficulty}</span>}
            {chal.solved_by_me && <span className="text-success">· Solved ✓</span>}
          </div>

          <Markdown>{chal.description}</Markdown>

          {chal.instancing === "per_team" ? (
            <InstancePanel slug={slug} instance={chal.instance} />
          ) : (
            chal.connection_info && (
              <div className="rounded-md border border-border bg-surface-2 p-3">
                <div className="mb-1 text-xs uppercase text-text-muted">Connection</div>
                <div className="flex items-center gap-2">
                  <code className="flex-1 font-mono text-sm text-text">{chal.connection_info}</code>
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => void navigator.clipboard.writeText(chal.connection_info ?? "")}
                  >
                    Copy
                  </Button>
                </div>
              </div>
            )
          )}

          {chal.attachments.length > 0 && (
            <div>
              <div className="mb-1 text-xs uppercase text-text-muted">Attachments</div>
              <ul className="space-y-1">
                {chal.attachments.map((a) => (
                  <li key={a.id}>
                    <a
                      className="text-primary underline"
                      href={`${API_BASE}/challenges/${slug}/attachments/${a.id}`}
                    >
                      {a.filename} ({Math.round(a.size_bytes / 1024)} KB)
                    </a>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {!chal.solved_by_me && (
            <form onSubmit={onSubmit} className="space-y-2">
              <div className="flex gap-2">
                <Input
                  data-testid="flag-input"
                  placeholder="OSCTF{...}"
                  value={flag}
                  onChange={(e) => { setFlag(e.target.value); }}
                  className={cn("font-mono", feedback.kind === "wrong" && "animate-shake border-danger")}
                  aria-label="Flag"
                />
                <Button data-testid="flag-submit" type="submit" disabled={submit.isPending || !flag}>
                  Submit
                </Button>
              </div>
              {chal.max_attempts != null && (
                <p className="text-xs text-text-muted">
                  Attempts: {chal.attempts_used} / {chal.max_attempts}
                </p>
              )}
              <FeedbackLine feedback={feedback} />
            </form>
          )}
        </div>
      )}
    </Dialog>
  );
}

function FeedbackLine({ feedback }: { feedback: Feedback }) {
  return (
    <div aria-live="polite" className="min-h-5 text-sm">
      {feedback.kind === "correct" && (
        <p className="text-success">
          Correct! ✓ {feedback.points != null && `+${feedback.points} pts`}
        </p>
      )}
      {feedback.kind === "wrong" && <p className="text-danger">Incorrect flag.</p>}
      {feedback.kind === "cooldown" && (
        <p className="text-warning">Too many attempts — wait a moment before trying again.</p>
      )}
      {feedback.kind === "error" && <p className="text-danger">{feedback.message}</p>}
    </div>
  );
}
