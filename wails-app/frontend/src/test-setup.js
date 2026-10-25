// Runs before every test file. In DOM tests, waitFor/findBy wait up to 15 s by default
// instead of 1 s; a test that names its own timeout is unaffected.
if (typeof document !== 'undefined') {
  const { configure } = await import('@testing-library/dom')
  configure({ asyncUtilTimeout: 15000 })
}
