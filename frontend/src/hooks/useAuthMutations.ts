import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { authApi, type LoginInput, type SignupInput } from '@/api/auth'
import { projectsApi } from '@/api/projects'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

async function afterAuth(
  res: { token: string; user_id: string; username: string },
  setSession: (token: string, user: { userId: string; username: string }) => void,
  setProjectId: (id: string) => void,
) {
  setSession(res.token, { userId: res.user_id, username: res.username })
  const project = await projectsApi.resolve({ folder_name: 'nexus-default' })
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
