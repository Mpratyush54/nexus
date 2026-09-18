import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AppShell } from '@/components/AppShell'
import { AgentsPage } from '@/pages/AgentsPage'
import { LandingPage } from '@/pages/LandingPage'
import { LoginPage } from '@/pages/LoginPage'
import { MemoryPage } from '@/pages/MemoryPage'
import { PlaceholderPage } from '@/pages/PlaceholderPage'
import { SettingsPage } from '@/pages/SettingsPage'
import { SignupPage } from '@/pages/SignupPage'
import { TeamPage } from '@/pages/TeamPage'
import { AppProviders } from '@/providers/AppProviders'
import { GuestRoute, ProtectedRoute } from '@/routes/guards'

export default function App() {
  return (
    <AppProviders>
      <BrowserRouter>
        <Routes>
          <Route path="/" element={<LandingPage />} />

          <Route element={<GuestRoute />}>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/signup" element={<SignupPage />} />
          </Route>

          <Route element={<ProtectedRoute />}>
            <Route path="/app" element={<AppShell />}>
              <Route index element={<Navigate to="memory" replace />} />
              <Route path="memory" element={<MemoryPage />} />
              <Route path="agents" element={<AgentsPage />} />
              <Route path="team" element={<TeamPage />} />
              <Route
                path="branches"
                element={
                  <PlaceholderPage
                    title="Branches"
                    blurb="Tree hierarchy with side-by-side diffs and merge preview."
                  />
                }
              />
              <Route
                path="sessions"
                element={
                  <PlaceholderPage
                    title="Sessions"
                    blurb="Handoff panel and agent steering HUD."
                    tone="amber"
                  />
                }
              />
              <Route path="settings" element={<SettingsPage />} />
            </Route>
          </Route>

          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </BrowserRouter>
    </AppProviders>
  )
}
