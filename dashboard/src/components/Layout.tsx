import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useEvent, useLogout, useMe } from "../api/hooks";
import { useTheme } from "../lib/theme";
import { cn } from "../lib/cn";

function NavItem({ to, children }: { to: string; children: string }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        cn(
          "rounded-md px-3 py-1.5 text-sm font-medium transition",
          isActive ? "bg-surface-2 text-text" : "text-text-muted hover:text-text",
        )
      }
    >
      {children}
    </NavLink>
  );
}

export function Layout() {
  const { data: me } = useMe();
  const { data: event } = useEvent();
  const { theme, toggle } = useTheme();
  const logout = useLogout();
  const navigate = useNavigate();

  return (
    <div className="flex min-h-full flex-col">
      <header className="sticky top-0 z-30 border-b border-border bg-surface/80 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-6xl items-center gap-2 px-4">
          <NavLink to="/" className="mr-2 font-bold text-text">
            {event?.name ?? "OSCTF"}
          </NavLink>
          <nav className="flex items-center gap-1">
            <NavItem to="/challenges">Challenges</NavItem>
            <NavItem to="/scoreboard">Scoreboard</NavItem>
            {me && <NavItem to="/team">Team</NavItem>}
            {me?.role === "admin" && <NavItem to="/admin">Admin</NavItem>}
          </nav>
          <div className="ml-auto flex items-center gap-2">
            <button
              onClick={toggle}
              className="rounded-md px-2 py-1.5 text-sm text-text-muted hover:text-text"
              aria-label="Toggle theme"
            >
              {theme === "dark" ? "☀" : "☾"}
            </button>
            {me ? (
              <div className="flex items-center gap-2">
                <NavLink to="/profile" className="text-sm text-text-muted hover:text-text">
                  {me.username}
                </NavLink>
                <button
                  onClick={() => {
                    logout.mutate(undefined, { onSuccess: () => void navigate("/") });
                  }}
                  className="rounded-md px-2 py-1.5 text-sm text-text-muted hover:text-text"
                >
                  Log out
                </button>
              </div>
            ) : (
              <div className="flex items-center gap-1">
                <NavItem to="/login">Log in</NavItem>
                <NavItem to="/register">Register</NavItem>
              </div>
            )}
          </div>
        </div>
      </header>
      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
        <Outlet />
      </main>
    </div>
  );
}
