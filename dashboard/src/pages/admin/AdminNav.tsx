import { NavLink } from "react-router-dom";
import { cn } from "../../lib/cn";

const links = [
  { to: "/admin", label: "Dashboard", end: true },
  { to: "/admin/event", label: "Event" },
  { to: "/admin/challenges", label: "Challenges" },
  { to: "/admin/users", label: "Users" },
  { to: "/admin/teams", label: "Teams" },
  { to: "/admin/submissions", label: "Submissions" },
];

export function AdminNav() {
  return (
    <nav className="mb-6 flex flex-wrap gap-1 border-b border-border pb-2">
      {links.map((l) => (
        <NavLink
          key={l.to}
          to={l.to}
          end={l.end}
          className={({ isActive }) =>
            cn(
              "rounded-md px-3 py-1.5 text-sm font-medium",
              isActive ? "bg-surface-2 text-text" : "text-text-muted hover:text-text",
            )
          }
        >
          {l.label}
        </NavLink>
      ))}
    </nav>
  );
}
