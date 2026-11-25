import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { authApi, type LoginInput, type SignupInput, type SignupSendCodeInput } from '@/api/auth'
import { projectsApi } from '@/api/projects'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

async function afterAuth(
  res: { token: string; user_id: string; username: string },
  setSession: (token: string, user: { userId: string; username: string }) => void,
  setProjectId: (id: string) => void,
) {
  setSession(res.token, { userId: res.user_id, username: res.username })
  // Returning users land on their own projects (useEnsureActiveProject picks
  // the busiest one). Only someone with no project at all gets a starter
  // one; resolving it on every login switched people to an empty project
  // and used up a slot of the plan's project limit.
  const existing = await projectsApi.list().catch(() => null)
  if (existing && existing.items?.length) return res
  // Projects resolve by folder name across all users and only the first
  // resolver becomes a member, so a shared name like 'nexus-default' left
  // every later signup on a project they could not open (403s everywhere).
  // The sign-in itself has succeeded by now: a failed starter project must
  // not turn it into an error on the form (the session is already stored).
  const project = await projectsApi.resolve({ folder_name: `${res.username}-personal` }).catch(() => null)
  if (project?.id) setProjectId(project.id)
  return res
}

export function useLogin() {
  const { setSession, setProjectId } = useAuth()
  const qc = useQueryClient()

  return useMutation({
    mutationFn: async (input: LoginInput) =>
      afterAuth(await authApi.login(input), setSession, setProjectId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
      void qc.invalidateQueries({ queryKey: queryKeys.project.current })
    },
  })
}

export function useSendSignupCode() {
  return useMutation({
    mutationFn: (input: SignupSendCodeInput) => authApi.sendSignupCode(input),
  })
}

export function useCancelSignup() {
  return useMutation({
    mutationFn: (email: string) => authApi.cancelSignup(email),
  })
}

export function useResendSignupCode() {
  return useMutation({
    mutationFn: (email: string) => authApi.resendSignupCode(email),
  })
}

export function useSignupStatus() {
  return useMutation({
    mutationFn: (email: string) => authApi.signupStatus(email),
  })
}

export function useSignup() {
  const { setSession, setProjectId } = useAuth()
  const qc = useQueryClient()

  return useMutation({
    mutationFn: async (input: SignupInput) =>
      afterAuth(await authApi.signup(input), setSession, setProjectId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
      void qc.invalidateQueries({ queryKey: queryKeys.project.current })
    },
  })
}

export function useForgotPassword() {
  return useMutation({
    mutationFn: (email: string) => authApi.forgotPassword(email),
  })
}

export function useResetPassword() {
  return useMutation({
    mutationFn: (input: { email: string; code: string; new_password: string }) =>
      authApi.resetPassword(input),
  })
}

export function useMe() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: ['users', 'me'],
    enabled: isAuthenticated,
    queryFn: () => authApi.me(),
  })
}

export function useUsage() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: ['users', 'me', 'usage'],
    enabled: isAuthenticated,
    queryFn: () => authApi.usage(),
  })
}

export function useTokens() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: ['auth', 'tokens'],
    enabled: isAuthenticated,
    queryFn: async () => (await authApi.listTokens()).items,
  })
}

export function useCreateToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: string | { name: string; agent_id?: string; scopes?: string[] }) => {
      if (typeof input === 'string') {
        return authApi.createToken({ name: input })
      }
      return authApi.createToken(input)
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['auth', 'tokens'] })
    },
  })
}

export function useRevokeToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => authApi.revokeToken(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['auth', 'tokens'] })
    },
  })
}
