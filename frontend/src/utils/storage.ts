const TOKEN_KEY = 'nexus.auth.token'
const USER_KEY = 'nexus.auth.user'
const PROJECT_KEY = 'nexus.project.id'
const ONBOARDING_KEY = 'nexus.onboarding.done'
const SIGNUP_DRAFT_KEY = 'nexus.signup.draft'

export type StoredUser = {
  userId: string
  username: string
}

export type SignupDraft = {
  email: string
  username: string
  step: 'details' | 'verify'
  savedAt: number
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

  getOnboardingDone(): boolean {
    return localStorage.getItem(ONBOARDING_KEY) === '1'
  },
  setOnboardingDone(done = true) {
    if (done) localStorage.setItem(ONBOARDING_KEY, '1')
    else localStorage.removeItem(ONBOARDING_KEY)
  },

  getSignupDraft(): SignupDraft | null {
    const raw = sessionStorage.getItem(SIGNUP_DRAFT_KEY)
    if (!raw) return null
    try {
      const d = JSON.parse(raw) as SignupDraft
      if (!d?.email || !d?.username) return null
      // Drafts older than 24h are dropped.
      if (Date.now() - (d.savedAt || 0) > 24 * 60 * 60 * 1000) {
        sessionStorage.removeItem(SIGNUP_DRAFT_KEY)
        return null
      }
      return d
    } catch {
      return null
    }
  },
  setSignupDraft(draft: Omit<SignupDraft, 'savedAt'>) {
    const payload: SignupDraft = { ...draft, savedAt: Date.now() }
    sessionStorage.setItem(SIGNUP_DRAFT_KEY, JSON.stringify(payload))
  },
  clearSignupDraft() {
    sessionStorage.removeItem(SIGNUP_DRAFT_KEY)
  },
}
