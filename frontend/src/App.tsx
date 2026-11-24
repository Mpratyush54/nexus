import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { lazy, Suspense } from "react";
import { AppShell } from "@/components/AppShell";
import { AppProviders } from "@/providers/AppProviders";
import { GuestRoute, ProtectedRoute } from "@/routes/guards";
import { HomeRedirect } from "@/routes/HomeRedirect";

// Every route page is its own chunk, so /login does not download the chat UI.
const ActivityPage = lazy(() => import("@/pages/ActivityPage").then((m) => ({ default: m.ActivityPage })));
const AdminPage = lazy(() => import("@/pages/AdminPage").then((m) => ({ default: m.AdminPage })));
const AgentsPage = lazy(() => import("@/pages/AgentsPage").then((m) => ({ default: m.AgentsPage })));
const BranchesPage = lazy(() => import("@/pages/BranchesPage").then((m) => ({ default: m.BranchesPage })));
const ChatsPage = lazy(() => import("@/pages/ChatsPage").then((m) => ({ default: m.ChatsPage })));
const CliAuthPage = lazy(() => import("@/pages/CliAuthPage").then((m) => ({ default: m.CliAuthPage })));
const ConnectPage = lazy(() => import("@/pages/ConnectPage").then((m) => ({ default: m.ConnectPage })));
const DashboardPage = lazy(() => import("@/pages/DashboardPage").then((m) => ({ default: m.DashboardPage })));
const ForgotPasswordPage = lazy(() => import("@/pages/ForgotPasswordPage").then((m) => ({ default: m.ForgotPasswordPage })));
const LoginPage = lazy(() => import("@/pages/LoginPage").then((m) => ({ default: m.LoginPage })));
const MemoryPage = lazy(() => import("@/pages/MemoryPage").then((m) => ({ default: m.MemoryPage })));
const NotificationsPage = lazy(() => import("@/pages/NotificationsPage").then((m) => ({ default: m.NotificationsPage })));
const OnboardingPage = lazy(() => import("@/pages/OnboardingPage").then((m) => ({ default: m.OnboardingPage })));
const OrgPage = lazy(() => import("@/pages/OrgPage").then((m) => ({ default: m.OrgPage })));
const SessionsPage = lazy(() => import("@/pages/SessionsPage").then((m) => ({ default: m.SessionsPage })));
const SettingsPage = lazy(() => import("@/pages/SettingsPage").then((m) => ({ default: m.SettingsPage })));
const SignupPage = lazy(() => import("@/pages/SignupPage").then((m) => ({ default: m.SignupPage })));
const TeamPage = lazy(() => import("@/pages/TeamPage").then((m) => ({ default: m.TeamPage })));
const TeleportPage = lazy(() => import("@/pages/TeleportPage").then((m) => ({ default: m.TeleportPage })));

/**
 * `basename` and `demo` are set only by src/demo/boot.tsx, which mounts the
 * same routes under /demo on sample data.
 */
export default function App({ basename, demo = false }: { basename?: string; demo?: boolean } = {}) {
  return (
    <AppProviders>
      <BrowserRouter basename={basename}>
        <Suspense
          fallback={
            <div role="status" className="p-8 text-sm text-fg-dim">
              Loading Nexus…
            </div>
          }
        >
          <Routes>
            <Route path="/" element={demo ? <Navigate to="/app/dashboard" replace /> : <HomeRedirect />} />
            <Route path="/cli/auth" element={<CliAuthPage />} />

            <Route element={<GuestRoute />}>
              <Route path="/login" element={<LoginPage />} />
              <Route path="/signup" element={<SignupPage />} />
              <Route path="/forgot-password" element={<ForgotPasswordPage />} />
            </Route>

            <Route element={<ProtectedRoute />}>
              <Route path="/app" element={<AppShell />}>
                <Route index element={<Navigate to="dashboard" replace />} />
                <Route path="connect" element={<ConnectPage />} />
                <Route path="onboarding" element={<OnboardingPage />} />
                <Route path="dashboard" element={<DashboardPage />} />
                <Route path="timeline" element={<ChatsPage />} />
                <Route path="chats" element={<ChatsPage />} />
                <Route path="teleport" element={<TeleportPage />} />
                <Route path="memory" element={<MemoryPage />} />
                <Route path="agents" element={<AgentsPage />} />
                <Route path="team" element={<TeamPage />} />
                <Route path="org" element={<OrgPage />} />
                <Route path="org/:orgId" element={<OrgPage />} />
                <Route path="branches" element={<BranchesPage />} />
                <Route path="sessions" element={<SessionsPage />} />
                <Route path="activity" element={<ActivityPage />} />
                <Route path="notifications" element={<NotificationsPage />} />
                <Route path="admin" element={<AdminPage />} />
                <Route path="settings" element={<SettingsPage />} />
                <Route path="*" element={<Navigate to="/app/dashboard" replace />} />
              </Route>
            </Route>

            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Suspense>
      </BrowserRouter>
    </AppProviders>
  );
}
