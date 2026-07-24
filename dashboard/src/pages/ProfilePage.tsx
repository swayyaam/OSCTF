import { useState, type SyntheticEvent } from "react";
import { useChangePassword, useMe } from "../api/hooks";
import { RequestError } from "../api/client";
import { useToast } from "../components/ui/toast";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Card, FieldError, Label } from "../components/ui/misc";

export function ProfilePage() {
  const { data: me } = useMe();
  const change = useChangePassword();
  const { toast } = useToast();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [err, setErr] = useState<RequestError | null>(null);

  const onSubmit = (e: SyntheticEvent) => {
    e.preventDefault();
    setErr(null);
    change.mutate(
      { current_password: current, new_password: next },
      {
        onSuccess: () => {
          setCurrent("");
          setNext("");
          toast({ title: "Password changed", variant: "success" });
        },
        onError: (e) => { setErr(e as RequestError); },
      },
    );
  };

  return (
    <div className="mx-auto max-w-md space-y-4">
      <div>
        <h1 className="text-2xl font-bold text-text">Settings</h1>
        {me && <p className="text-sm text-text-muted">{me.email}</p>}
      </div>
      <Card className="space-y-3">
        <h2 className="font-semibold text-text">Change password</h2>
        {err && !err.api.fields && <p className="text-sm text-danger">{err.api.detail}</p>}
        <form onSubmit={onSubmit} className="space-y-3">
          <div>
            <Label htmlFor="current">Current password</Label>
            <Input
              id="current"
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => { setCurrent(e.target.value); }}
              required
            />
          </div>
          <div>
            <Label htmlFor="next">New password</Label>
            <Input
              id="next"
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(e) => { setNext(e.target.value); }}
              required
            />
            <FieldError messages={err?.api.fields?.new_password} />
          </div>
          <Button type="submit" disabled={change.isPending}>
            Update password
          </Button>
        </form>
      </Card>
    </div>
  );
}
