/** True inside /demo (src/demo/boot.tsx marks <html> with nx-demo). */
export function useIsDemo() {
  return typeof document !== 'undefined' && document.documentElement.classList.contains('nx-demo')
}
