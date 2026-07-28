import { createBrowserRouter } from "react-router-dom";
import { Layout } from "./components/Layout";
import { RedirectIfAuthed, RequireAdmin, RequireAuth } from "./components/guards";
import { LandingPage } from "./pages/LandingPage";
import { LoginPage } from "./pages/LoginPage";
import { RegisterPage } from "./pages/RegisterPage";
import { ChallengesPage } from "./pages/ChallengesPage";
import { ScoreboardPage } from "./pages/ScoreboardPage";
import { TeamPage } from "./pages/TeamPage";
import { PublicTeamPage } from "./pages/PublicTeamPage";
import { PublicUserPage } from "./pages/PublicUserPage";
import { ProfilePage } from "./pages/ProfilePage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { AdminDashboard } from "./pages/admin/AdminDashboard";
import { AdminEventPage } from "./pages/admin/AdminEventPage";
import { AdminChallengesPage } from "./pages/admin/AdminChallengesPage";
import { AdminChallengeEditor } from "./pages/admin/AdminChallengeEditor";
import { AdminInstancesPage } from "./pages/admin/AdminInstancesPage";
import { AdminUsersPage } from "./pages/admin/AdminUsersPage";
import { AdminTeamsPage } from "./pages/admin/AdminTeamsPage";
import { AdminSubmissionsPage } from "./pages/admin/AdminSubmissionsPage";

export const router = createBrowserRouter([
  {
    element: <Layout />,
    children: [
      { path: "/", element: <LandingPage /> },
      { path: "/scoreboard", element: <ScoreboardPage /> },
      { path: "/teams/:id", element: <PublicTeamPage /> },
      { path: "/users/:id", element: <PublicUserPage /> },
      {
        element: <RedirectIfAuthed />,
        children: [
          { path: "/login", element: <LoginPage /> },
          { path: "/register", element: <RegisterPage /> },
        ],
      },
      {
        element: <RequireAuth />,
        children: [
          { path: "/challenges", element: <ChallengesPage /> },
          { path: "/challenges/:slug", element: <ChallengesPage /> },
          { path: "/team", element: <TeamPage /> },
          { path: "/profile", element: <ProfilePage /> },
        ],
      },
      {
        element: <RequireAdmin />,
        children: [
          { path: "/admin", element: <AdminDashboard /> },
          { path: "/admin/event", element: <AdminEventPage /> },
          { path: "/admin/challenges", element: <AdminChallengesPage /> },
          { path: "/admin/challenges/new", element: <AdminChallengeEditor /> },
          { path: "/admin/challenges/:id", element: <AdminChallengeEditor /> },
          { path: "/admin/instances", element: <AdminInstancesPage /> },
          { path: "/admin/users", element: <AdminUsersPage /> },
          { path: "/admin/teams", element: <AdminTeamsPage /> },
          { path: "/admin/submissions", element: <AdminSubmissionsPage /> },
        ],
      },
      { path: "*", element: <NotFoundPage /> },
    ],
  },
]);
