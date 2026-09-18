import { QueryClientProvider } from '@tanstack/react-query'
import { ReactQueryDevtools } from '@tanstack/react-query-devtools'
import { useState, type ReactNode } from 'react'
import { createQueryClient } from '@/lib/query-client'
import { AuthProvider } from '@/providers/AuthProvider'
import { ToastProvider } from '@/components/ui/Toast'

export function AppProviders({ children }: { children: ReactNode }) {
  const [client] = useState(() => createQueryClient())

  return (
    <QueryClientProvider client={client}>
      <AuthProvider>
        <ToastProvider>{children}</ToastProvider>
      </AuthProvider>
      <ReactQueryDevtools initialIsOpen={false} buttonPosition="bottom-left" />
    </QueryClientProvider>
  )
}
