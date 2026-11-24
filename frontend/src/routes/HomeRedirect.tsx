import { useEffect } from 'react'

/** "/" is a static, prerendered page outside the React app: leave the SPA to load it. */
export function HomeRedirect() {
  useEffect(() => {
    window.location.replace('/')
  }, [])
  return null
}
