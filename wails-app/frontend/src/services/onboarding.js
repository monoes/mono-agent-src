// The first-run onboarding's bindings (wails-app/app_onboarding.go,
// app_chromium.go, app_browser_permissions.go). Thin on purpose: each returns
// the binding's own answer, unparsed and uncaught, so onboarding/callStep.js
// sees every failure in the style the binding gave it. Kept out of api.js,
// which is past the 500-line limit.
import * as GoApp from '../wailsjs/go/main/App'

export const onboarding = {
  // true once the backend finished startup (StartupOverlay polls the same)
  ready: () => GoApp.IsReady(),
  // { completed, currentStep, profileId, steps: [{ id, status }] }
  state: () => GoApp.GetOnboardingState(),
  // the same, with every step's facts read even on a completed install
  stateFull: () => GoApp.GetOnboardingStateFull(),
  mark: (id) => GoApp.MarkOnboardingStep(id),
  skip: (id) => GoApp.SkipOnboardingStep(id),
  complete: () => GoApp.CompleteOnboarding(),
  restart: () => GoApp.RestartOnboarding(),
  // [{ id, name, installed, extensionsUrl, devModeSide }]
  detectBrowsers: () => GoApp.DetectChromiumBrowsers(),
  openBrowser: (id) => GoApp.OpenChromiumBrowser(id),
  // preset: 'ask' | 'free' | 'off'; the CLI's JSON or {"error", "code"}
  applyPermissionPreset: (preset) => GoApp.ApplyBrowserPermissionPreset(preset),
}
