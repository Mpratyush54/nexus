import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { storage, type StoredUser } from '@/utils/storage'

type AuthState = {
  token: string | null
  user: StoredUser | null
  projectId: string | null
  isAuthenticated: boolean
  setSession: (token: string, user: StoredUser) => void
  setProjectId: (id: string) => void
  logout: () => void
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(() => storage.getToken())
  const [user, setUser] = useState<StoredUser | null>(() => storage.getUser())
  const [projectId, setProjectIdState] = useState<string | null>(() =>
    storage.getProjectId(),
  )

  const setSession = useCallback((nextToken: string, nextUser: StoredUser) => {
    storage.setToken(nextToken)
    storage.setUser(nextUser)
    setToken(nextToken)
    setUser(nextUser)
  }, [])

  const setProjectId = useCallback((id: string) => {
    storage.setProjectId(id)
    setProjectIdState(id)
  }, [])

  const logout = useCallback(() => {
    storage.clearSession()
    setToken(null)
    setUser(null)
    setProjectIdState(null)
  }, [])

  const value = useMemo(
    () => ({
      token,
      user,
      projectId,
      isAuthenticated: Boolean(token),
      setSession,
      setProjectId,
      logout,
    }),
    [token, user, projectId, setSession, setProjectId, logout],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
