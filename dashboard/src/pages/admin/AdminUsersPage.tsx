import { useState } from "react";
import { Link } from "react-router-dom";
import { useAdminUsers, useResetPassword, useUpdateUser, type UserFilters } from "../../api/admin-hooks";
import type { components } from "../../api/schema";
import { useToast } from "../../components/ui/toast";
import { AdminNav } from "./AdminNav";
import { Input } from "../../components/ui/input";
import { Button } from "../../components/ui/button";
import { Table, Td, Th } from "../../components/ui/table";
import { Skeleton } from "../../components/ui/misc";

type UserAdmin = components["schemas"]["UserAdmin"];

export function AdminUsersPage() {
  const [filters, setFilters] = useState<UserFilters>({ page: 1, per_page: 50 });
  const { data, isLoading } = useAdminUsers(filters);

  return (
    <div>
      <AdminNav />
      <h1 className="mb-4 text-2xl font-bold text-text">Users</h1>
      <Input
        placeholder="Search username or email…"
        className="mb-3 max-w-sm"
        onChange={(e) => { setFilters({ ...filters, q: e.target.value || undefined, page: 1 }); }}
      />
      {isLoading || !data ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Username</Th>
              <Th>Email</Th>
              <Th>Team</Th>
              <Th>Role</Th>
              <Th>Flags</Th>
              <Th>Actions</Th>
            </tr>
          </thead>
          <tbody>
            {data.items.map((u) => (
              <UserRow key={u.id} user={u} />
            ))}
          </tbody>
        </Table>
      )}
      {data && <p className="mt-2 text-xs text-text-muted">{data.total} users</p>}
    </div>
  );
}

function UserRow({ user }: { user: UserAdmin }) {
  const update = useUpdateUser();
  const reset = useResetPassword();
  const { toast } = useToast();
  return (
    <tr>
      <Td>
        <Link to={`/users/${user.id}`} className="text-primary">
          {user.username}
        </Link>
      </Td>
      <Td className="text-text-muted">{user.email}</Td>
      <Td className="text-text-muted">{user.team?.name ?? "—"}</Td>
      <Td>
        <select
          className="rounded border border-border bg-surface px-1 text-sm text-text"
          value={user.role}
          onChange={(e) =>
            { update.mutate({
              id: user.id,
              body: { role: e.target.value as components["schemas"]["Role"] },
            }); }
          }
        >
          <option value="user">user</option>
          <option value="admin">admin</option>
        </select>
      </Td>
      <Td className="space-x-2 text-xs">
        <button
          className={user.banned ? "text-danger" : "text-text-muted"}
          onClick={() => { update.mutate({ id: user.id, body: { banned: !user.banned } }); }}
        >
          {user.banned ? "banned" : "ban"}
        </button>
        <button
          className={user.hidden ? "text-warning" : "text-text-muted"}
          onClick={() => { update.mutate({ id: user.id, body: { hidden: !user.hidden } }); }}
        >
          {user.hidden ? "hidden" : "hide"}
        </button>
      </Td>
      <Td>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => {
            const pw = prompt(`New password for ${user.username}:`);
            if (pw) {
              reset.mutate(
                { id: user.id, new_password: pw },
                { onSuccess: () => { toast({ title: "Password reset", variant: "success" }); } },
              );
            }
          }}
        >
          Reset password
        </Button>
      </Td>
    </tr>
  );
}
