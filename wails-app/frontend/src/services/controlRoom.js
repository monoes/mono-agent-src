// The control room bindings (wails-app/app_doomsday.go). Each call resolves to the CLI's JSON (`doomsday overview`,
// `doomsday feed`, `doomsday stop`) or to {error} and never rejects. There is no start, approve, pause or resume
// here: those are the terminal's. stop takes no argument.
import * as GoApp from '../wailsjs/go/main/App'

const run = (call) => Promise.resolve()
  .then(call)
  .then(r => (typeof r === 'string' ? JSON.parse(r) : r))
  .catch(e => ({ error: e?.message || String(e) }))

export const controlRoomApi = {
  // {mode_available, run_exists}; both false on any failure.
  probe:    () => run(() => GoApp.DoomsdayProbe()),
  overview: () => run(() => GoApp.DoomsdayOverview()),
  // before 0 = newest page; limit 0 = the CLI's default.
  feed:     (before = 0, limit = 0) => run(() => GoApp.DoomsdayFeed(before, limit)),
  // {ok, stopped, output, detail, code}
  stop:     () => run(() => GoApp.DoomsdayStop()),
}
