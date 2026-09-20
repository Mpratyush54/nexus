import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AppShell } from '@/components/AppShell'
import { ActivityPage } from '@/pages/ActivityPage'
import { AdminPage } from '@/pages/AdminPage'
import { AgentsPage } from '@/pages/AgentsPage'
import { BranchesPage } from '@/pages/BranchesPage'
import { CliAuthPage } from '@/pages/CliAuthPage'
import { ConnectPage } from '@/pages/ConnectPage'
import { DashboardPage } from '@/pages/DashboardPage'
import { LandingPage } from '@/pages/LandingPage'
import { LoginPage } from '@/pages/LoginPage'
import { MemoryPage } from '@/pages/MemoryPage'
import { NotificationsPage } from '@/pages/NotificationsPage'
import { OnboardingPage } from '@/pages/OnboardingPage'
import { SessionsPage } from '@/pages/SessionsPage'
import { SettingsPage } from '@/pages/SettingsPage'
import { SignupPage } from '@/pages/SignupPage'
import { ForgotPasswordPage } from '@/pages/ForgotPasswordPage'
import { TeamPage } from '@/pages/TeamPage'
import { OrgPage } from '@/pages/OrgPage'
import { AppProviders } from '@/providers/AppProviders'
import { GuestRoute, ProtectedRoute } from '@/routes/guards'

export default function App() {
  return (
    <AppProviders>
      <BrowserRouter>
        <Routes>
          <Route path="/" element={<LandingPage />} />
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
            </Route>
          </Route>

          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </BrowserRouter>
    </AppProviders>
  )
}
