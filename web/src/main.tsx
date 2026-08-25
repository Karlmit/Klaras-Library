import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App } from './App'
import './styles/app.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The catalogue changes rarely from the browser's point of view, and
      // covers are immutable, so refetching on every window focus is pure noise.
      refetchOnWindowFocus: false,
      staleTime: 30_000,
      retry: (count, err) => {
        // Never retry an auth failure into a loop.
        const status = (err as { status?: number }).status
        if (status === 401 || status === 403) return false
        return count < 2
      },
    },
  },
})

const el = document.getElementById('root')
if (!el) throw new Error('#root missing')

createRoot(el).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </React.StrictMode>,
)
