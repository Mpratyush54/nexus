// One index.html serves every route. The landing page at "/" is prerendered
// static HTML with a tiny script; every other route loads the React app.
if (window.location.pathname === '/') {
  void import('./landing/boot')
} else {
  void import('./app-boot')
}
