const TOKEN_KEY = 'nexus.auth.token'
const USER_KEY = 'nexus.auth.user'
const PROJECT_KEY = 'nexus.project.id'

export type StoredUser = {
  userId: string
  username: string
}

export const storage = {
  getToken(): string | null {
    return localStorage.getItem(TOKEN_KEY)
  },
  setToken(token: string) {
    localStorage.setItem(TOKEN_KEY, token)
  },
  clearToken() {
    localStorage.removeItem(TOKEN_KEY)
  },

  getUser(): StoredUser | null {
    const raw = localStorage.getItem(USER_KEY)
    if (!raw) return null
    try {
      return JSON.parse(raw) as StoredUser
    } catch {
      return null
    }
  },
  setUser(user: StoredUser) {
    localStorage.setItem(USER_KEY, JSON.stringify(user))
  },
  clearUser() {
    localStorage.removeItem(USER_KEY)
  },

  getProjectId(): string | null {
    return localStorage.getItem(PROJECT_KEY)
  },
  setProjectId(id: string) {
    localStorage.setItem(PROJECT_KEY, id)
  },
  clearProjectId() {
    localStorage.removeItem(PROJECT_KEY)
  },

  clearSession() {
    this.clearToken()
    this.clearUser()
    this.clearProjectId()
  },
}
