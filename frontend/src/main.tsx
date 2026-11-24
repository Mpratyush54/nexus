// One index.html serves every route. The landing page at "/" is prerendered
// static HTML with a tiny script; every other route loads the React app.
const path = window.location.pathname
if (path === '/') {
  void import('./landing/boot')
} else if (path === '/demo' || path.startsWith('/demo/')) {
  // The interactive demo: the same app on in-browser sample data (src/demo).
  void import('./demo/boot')
} else {
  void import('./app-boot')
}
