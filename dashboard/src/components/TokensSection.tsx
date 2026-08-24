import { useState, type SyntheticEvent } from "react";
import { useCreateToken, useRevokeToken, useTokens } from "../api/hooks";
import { RequestError } from "../api/client";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Card, FieldError, Label } from "./ui/misc";
import { formatLocal } from "../lib/time";
import type { components } from "../api/schema";

type Scope = components["schemas"]["TokenScope"];

const SCOPES: { value: Scope; label: string; hint: string }[] = [
  { value: "read", label: "read", hint: "GET requests" },
  { value: "submit", label: "submit", hint: "writes: submitting flags, teams" },
  { value: "admin", label: "admin", hint: "admin endpoints — never exceeds your own role" },
];

/**
 * API tokens for non-browser clients. The plaintext appears exactly once, at creation: the server
 * stores only a hash, so this panel shows it in a copy-once box rather than pretending it can be
 * retrieved later.
 */
export function TokensSection() {
  const { data: tokens } = useTokens();
  const create = useCreateToken();
  const revoke = useRevokeToken();

  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<Scope[]>(["read"]);
  const [plaintext, setPlaintext] = useState<string | null>(null);
  const [err, setErr] = useState<RequestError | null>(null);

  const toggle = (s: Scope) => {
    setScopes((cur) => (cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s]));
  };

  const onSubmit = (e: SyntheticEvent) => {
    e.preventDefault();
    setErr(null);
    setPlaintext(null);
    create.mutate(
      { name, scopes },
      {
        onSuccess: (t) => {
          setPlaintext(t.token);
          setName("");
        },
        onError: (e) => {
          setErr(e as RequestError);
        },
      },
    );
  };

  return (
    <Card className="space-y-4">
      <div>
        <h2 className="text-lg font-semibold text-text">API tokens</h2>
        <p className="text-sm text-text-muted">
          For clients that are not this browser — scripts, CI, integrations. A token carries your
          own role at most, so an <code>admin</code> scope on a non-admin account grants nothing.
        </p>
      </div>

      {plaintext && (
        <div className="rounded-md border border-warning bg-warning/10 p-3">
          <p className="mb-2 text-sm font-medium text-text">
            Copy this now — it is shown once and cannot be retrieved again.
          </p>
          <code className="block break-all rounded bg-surface-2 p-2 font-mono text-sm text-text">
            {plaintext}
          </code>
        </div>
      )}

      <form onSubmit={onSubmit} className="space-y-3">
        <div>
          <Label htmlFor="token-name">Name</Label>
          <Input
            id="token-name"
            value={name}
            placeholder="ci-pipeline"
            onChange={(e) => {
              setName(e.target.value);
            }}
            required
          />
          <FieldError messages={err?.api.fields?.name} />
        </div>
        <fieldset>
          <legend className="mb-1 text-sm font-medium text-text">Scopes</legend>
          <div className="space-y-1">
            {SCOPES.map((s) => (
              <label key={s.value} className="flex items-center gap-2 text-sm text-text-muted">
                <input
                  type="checkbox"
                  checked={scopes.includes(s.value)}
                  onChange={() => {
                    toggle(s.value);
                  }}
                />
                <span className="font-mono text-text">{s.label}</span>
                <span>— {s.hint}</span>
              </label>
            ))}
          </div>
          <FieldError messages={err?.api.fields?.scopes} />
        </fieldset>
        <Button type="submit" disabled={create.isPending || scopes.length === 0}>
          {create.isPending ? "Creating…" : "Create token"}
        </Button>
      </form>

      {tokens && tokens.length > 0 && (
        <ul className="divide-y divide-border border-t border-border">
          {tokens.map((t) => (
            <li key={t.id} className="flex items-center justify-between gap-3 py-2">
              <div className="min-w-0">
                <p className="truncate text-sm font-medium text-text">{t.name}</p>
                <p className="text-xs text-text-muted">
                  <span className="font-mono">{t.prefix}…</span> · {t.scopes.join(", ")} · created{" "}
                  {formatLocal(t.created_at)}
                  {t.last_used_at ? ` · last used ${formatLocal(t.last_used_at)}` : " · never used"}
                </p>
              </div>
              <Button
                variant="danger"
                onClick={() => {
                  revoke.mutate(t.id);
                }}
                disabled={revoke.isPending}
              >
                Revoke
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
