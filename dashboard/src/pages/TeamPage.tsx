import { useState, type SyntheticEvent } from "react";
import {
  useCreateTeam,
  useJoinTeam,
  useLeaveTeam,
  useMe,
  usePublicTeam,
  useRegenInvite,
  useRenameTeam,
} from "../api/hooks";
import { RequestError } from "../api/client";
import { useToast } from "../components/ui/toast";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Card, FieldError, Label, Skeleton } from "../components/ui/misc";
import { SolveList } from "../components/SolveList";

export function TeamPage() {
  const { data: me, isLoading } = useMe();
  if (isLoading) return <Skeleton className="h-40 w-full" />;
  return me?.team ? <MyTeam teamId={me.team.id} isCaptain={me.team.role === "captain"} /> : <NoTeam />;
}

function NoTeam() {
  const create = useCreateTeam();
  const join = useJoinTeam();
  const [name, setName] = useState("");
  const [code, setCode] = useState("");
  const [createErr, setCreateErr] = useState<RequestError | null>(null);
  const [joinErr, setJoinErr] = useState<RequestError | null>(null);

  const onCreate = (e: SyntheticEvent) => {
    e.preventDefault();
    setCreateErr(null);
    create.mutate({ name }, { onError: (e) => { setCreateErr(e as RequestError); } });
  };
  const onJoin = (e: SyntheticEvent) => {
    e.preventDefault();
    setJoinErr(null);
    join.mutate({ invite_code: code }, { onError: (e) => { setJoinErr(e as RequestError); } });
  };

  return (
    <div className="mx-auto grid max-w-3xl gap-4 md:grid-cols-2">
      <Card className="space-y-3">
        <h2 className="font-semibold text-text">Create a team</h2>
        <form onSubmit={onCreate} className="space-y-2">
          <div>
            <Label htmlFor="team-name">Team name</Label>
            <Input id="team-name" value={name} onChange={(e) => { setName(e.target.value); }} required />
            <FieldError messages={createErr?.api.fields?.name} />
            {createErr && !createErr.api.fields && (
              <p className="mt-1 text-sm text-danger">{createErr.api.detail}</p>
            )}
          </div>
          <Button type="submit" disabled={create.isPending}>
            Create
          </Button>
        </form>
      </Card>
      <Card className="space-y-3">
        <h2 className="font-semibold text-text">Join a team</h2>
        <form onSubmit={onJoin} className="space-y-2">
          <div>
            <Label htmlFor="invite">Invite code</Label>
            <Input
              id="invite"
              value={code}
              onChange={(e) => { setCode(e.target.value.toUpperCase()); }}
              className="font-mono"
              required
            />
            {joinErr && <p className="mt-1 text-sm text-danger">{joinErr.api.detail}</p>}
          </div>
          <Button type="submit" variant="secondary" disabled={join.isPending}>
            Join
          </Button>
        </form>
      </Card>
    </div>
  );
}

function MyTeam({ teamId, isCaptain }: { teamId: string; isCaptain: boolean }) {
  const { data: team, isLoading } = usePublicTeam(teamId);
  const leave = useLeaveTeam();
  const rename = useRenameTeam(teamId);
  const regen = useRegenInvite(teamId);
  const { toast } = useToast();
  const [newName, setNewName] = useState("");

  if (isLoading || !team) return <Skeleton className="h-40 w-full" />;

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-text">{team.name}</h1>
        <div className="text-right">
          <div className="font-mono text-xl text-primary">{team.points} pts</div>
          {team.rank != null && <div className="text-sm text-text-muted">Rank #{team.rank}</div>}
        </div>
      </div>

      {team.invite_code && (
        <Card className="space-y-2">
          <Label>Invite code</Label>
          <div className="flex items-center gap-2">
            <code data-testid="invite-code" className="flex-1 font-mono text-lg text-text">
              {team.invite_code}
            </code>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => void navigator.clipboard.writeText(team.invite_code ?? "")}
            >
              Copy
            </Button>
            {isCaptain && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() =>
                  { regen.mutate(undefined, {
                    onSuccess: () => { toast({ title: "Invite code regenerated", variant: "success" }); },
                  }); }
                }
              >
                Regenerate
              </Button>
            )}
          </div>
        </Card>
      )}

      <Card>
        <h2 className="mb-2 font-semibold text-text">Members</h2>
        <ul className="space-y-1 text-sm">
          {team.members.map((m) => (
            <li key={m.id} className="text-text-muted">
              {m.username}
            </li>
          ))}
        </ul>
      </Card>

      {isCaptain && (
        <Card className="space-y-2">
          <Label htmlFor="rename">Rename team</Label>
          <div className="flex gap-2">
            <Input
              id="rename"
              placeholder={team.name}
              value={newName}
              onChange={(e) => { setNewName(e.target.value); }}
            />
            <Button
              variant="secondary"
              disabled={!newName || rename.isPending}
              onClick={() =>
                { rename.mutate(
                  { name: newName },
                  {
                    onSuccess: () => {
                      setNewName("");
                      toast({ title: "Team renamed", variant: "success" });
                    },
                    onError: (e) => { toast({ title: (e as RequestError).api.detail ?? "Failed", variant: "danger" }); },
                  },
                ); }
              }
            >
              Rename
            </Button>
          </div>
        </Card>
      )}

      <SolveList solves={team.solves} showSolver />

      <Button
        variant="danger"
        onClick={() => {
          if (confirm("Leave this team?")) leave.mutate();
        }}
      >
        Leave team
      </Button>
    </div>
  );
}
