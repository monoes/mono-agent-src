### Task 3: The JS bindings and the task service

**Files:**
- Modify: `wails-app/frontend/src/wailsjs/go/main/App.js`, `wails-app/frontend/src/wailsjs/go/main/App.d.ts` (eleven entries each)
- Create: `wails-app/frontend/src/services/tasks.js`
- Test: `wails-app/frontend/src/services/tasks.test.js`

**Interfaces:**
- Consumes: Tasks 1 and 2's bound methods; `subscribeEvent(name, callback)` from `services/api.js` (returns the unsubscribe function; a no-op outside the desktop shell).
- Produces (`services/tasks.js`; every later task imports these names):
  - `DONE_LIMIT = 50`
  - `tasksApi.board()`, `.show(id)`, `.add(spec)`, `.edit(id, change)`, `.move(id, status, place)`, `.approve(ids, top)`, `.archive(ids)`, `.unarchive(ids)`, `.comment(id, text)`, `.pulse()`: each resolves to the CLI's parsed JSON or `{error, code?}` and never rejects. `place` is `{where: '' | 'top' | 'bottom' | 'before' | 'after', ref}`.
  - `tasksApi.agentShell()`: resolves to the marker name or `''`.
  - `onTasksChanged(callback)`: subscribes to `tasks:changed`, returns the unsubscribe function.

- [ ] **Step 1: Write the failing test**

Create `wails-app/frontend/src/services/tasks.test.js`:

```js
// The bindings the task service calls exist in the generated module. They
// are placed there by hand (phase 3 plan, Task 3): a misspelt name would
// only fail when a person clicks.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import * as App from '../wailsjs/go/main/App'

const service = readFileSync(join(__dirname, 'tasks.js'), 'utf8')
const used = [...new Set([...service.matchAll(/GoApp\.(\w+)/g)].map(m => m[1]))].sort()
const dts = readFileSync(join(__dirname, '..', 'wailsjs', 'go', 'main', 'App.d.ts'), 'utf8')

describe('task bindings', () => {
  it('finds the eleven bindings the service uses', () => {
    expect(used).toEqual(['TaskAdd', 'TaskAgentShell', 'TaskApprove', 'TaskArchive', 'TaskBoard', 'TaskComment',
      'TaskEdit', 'TaskMove', 'TaskPulse', 'TaskShow', 'TaskUnarchive'])
  })
  it('has each in App.js and App.d.ts', () => {
    expect(used.filter(n => typeof App[n] !== 'function')).toEqual([])
    expect(used.filter(n => !dts.includes(`export function ${n}(`))).toEqual([])
  })
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `npm --prefix wails-app/frontend test -- src/services/tasks.test.js`
Expected: FAIL (`ENOENT` reading `tasks.js`).

- [ ] **Step 3: Add the entries to the generated bindings**

Do not run `wails generate`. Find the place with `grep -n "TagApplication\|TestAutomation" wails-app/frontend/src/wailsjs/go/main/App.js wails-app/frontend/src/wailsjs/go/main/App.d.ts`. In `App.js`, insert between the end of the `TagApplication` function and `export function TestAutomation(`, one blank line between functions as the file has:

```js
export function TaskAdd(arg1) {
  return window['go']['main']['App']['TaskAdd'](arg1);
}

export function TaskAgentShell() {
  return window['go']['main']['App']['TaskAgentShell']();
}

export function TaskApprove(arg1, arg2) {
  return window['go']['main']['App']['TaskApprove'](arg1, arg2);
}

export function TaskArchive(arg1) {
  return window['go']['main']['App']['TaskArchive'](arg1);
}

export function TaskBoard(arg1) {
  return window['go']['main']['App']['TaskBoard'](arg1);
}

export function TaskComment(arg1, arg2) {
  return window['go']['main']['App']['TaskComment'](arg1, arg2);
}

export function TaskEdit(arg1, arg2) {
  return window['go']['main']['App']['TaskEdit'](arg1, arg2);
}

export function TaskMove(arg1, arg2, arg3, arg4) {
  return window['go']['main']['App']['TaskMove'](arg1, arg2, arg3, arg4);
}

export function TaskPulse() {
  return window['go']['main']['App']['TaskPulse']();
}

export function TaskShow(arg1) {
  return window['go']['main']['App']['TaskShow'](arg1);
}

export function TaskUnarchive(arg1) {
  return window['go']['main']['App']['TaskUnarchive'](arg1);
}
```

In `App.d.ts`, at the same place:

```ts
export function TaskAdd(arg1:string):Promise<string>;

export function TaskAgentShell():Promise<string>;

export function TaskApprove(arg1:Array<number>,arg2:boolean):Promise<string>;

export function TaskArchive(arg1:Array<number>):Promise<string>;

export function TaskBoard(arg1:number):Promise<string>;

export function TaskComment(arg1:number,arg2:string):Promise<string>;

export function TaskEdit(arg1:number,arg2:string):Promise<string>;

export function TaskMove(arg1:number,arg2:string,arg3:string,arg4:number):Promise<string>;

export function TaskPulse():Promise<Record<string, any>>;

export function TaskShow(arg1:number):Promise<string>;

export function TaskUnarchive(arg1:Array<number>):Promise<string>;
```

`models.ts` does not change: no binding returns a struct. These types are spelt as the generator spells them, so the next `wails dev` or `wails build` on the user's machine rewrites nothing: check with `grep -n "GetExecutionDetail\|GetPeopleTagsMap" wails-app/frontend/src/wailsjs/go/main/App.d.ts` that a `map[string]interface{}` result is written `Promise<Record<string, any>>` and a slice argument `Array<…>` (so `[]int64` is `Array<number>`), and copy any difference you see.

- [ ] **Step 4: Write `wails-app/frontend/src/services/tasks.js`**

```js
// The task board bindings (wails-app/app_tasks.go, app_tasks_watch.go).
// Each call resolves to the CLI's JSON (`monoagentcli task … --json`; the
// board, read in process, comes in the same shape) or to {error, code} and
// never rejects: the board shows a refusal on the card it
// concerns instead of a page-wide failure.
import * as GoApp from '../wailsjs/go/main/App'
import { subscribeEvent } from './api.js'

// The Done column shows the most recent DONE_LIMIT cards (spec §10).
export const DONE_LIMIT = 50

// run takes a thunk so a binding missing from an older app build (or from a
// test's mock) becomes {error} too, not a synchronous throw.
const run = (call) => Promise.resolve()
  .then(call)
  .then(r => (typeof r === 'string' ? JSON.parse(r) : r))
  .catch(e => ({ error: e?.message || String(e) }))

export const tasksApi = {
  board:     () => run(() => GoApp.TaskBoard(DONE_LIMIT)),
  show:      (id) => run(() => GoApp.TaskShow(id)),
  // spec: {title, notes} or {text}; ready: straight to Ready.
  add:       (spec) => run(() => GoApp.TaskAdd(JSON.stringify(spec))),
  // change: {title} and/or {notes}; empty notes clear them.
  edit:      (id, change) => run(() => GoApp.TaskEdit(id, JSON.stringify(change))),
  // place: {where: '' | 'top' | 'bottom' | 'before' | 'after', ref}
  move:      (id, status, place = {}) => run(() => GoApp.TaskMove(id, status, place.where || '', place.ref || 0)),
  approve:   (ids, top = false) => run(() => GoApp.TaskApprove(ids, top)),
  archive:   (ids) => run(() => GoApp.TaskArchive(ids)),
  unarchive: (ids) => run(() => GoApp.TaskUnarchive(ids)),
  comment:   (id, text) => run(() => GoApp.TaskComment(id, text)),
  // {profile_id, rev, inbox, review}, or {} before the database is open or for a profile that is not there.
  pulse:     () => run(() => GoApp.TaskPulse()),
  // The agent-context marker the app inherited, or '' (a plain string, not JSON).
  agentShell: () => Promise.resolve().then(() => GoApp.TaskAgentShell()).then(m => m || '').catch(() => ''),
}

// onTasksChanged: the active profile's board revision moved. Payload
// {profile_id, rev, inbox, review}. Returns the unsubscribe function.
export function onTasksChanged(callback) {
  return subscribeEvent('tasks:changed', callback)
}
```

- [ ] **Step 5: Run the test to see it pass**

Run: `npm --prefix wails-app/frontend test -- src/services/tasks.test.js`
Expected: PASS (two tests). Then misspell one entry in `App.js` (`TaskMov`), run again, see "has each in App.js and App.d.ts" FAIL, and undo.

- [ ] **Step 6: Commit**

```
git add wails-app/frontend/src/wailsjs/go/main/App.js wails-app/frontend/src/wailsjs/go/main/App.d.ts wails-app/frontend/src/services/tasks.js wails-app/frontend/src/services/tasks.test.js
```
then
```
git commit -m "feat(tasks): JS bindings and the task service for the desktop board" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 7: Compare with what the generator writes**

Release builds regenerate these files (`wails build -skipbindings=false`), so the hand-placed lines should be exactly what Wails writes. Run `cd wails-app && wails generate module` (the local `wails` is v2.11 while `go.mod` pins v2.16, so it may also rewrite entries this phase did not touch), then `git diff -U0 -G Task -- wails-app/frontend/src/wailsjs`.
Expected: no output (no added or removed line holds `Task`: the generator wrote this phase's eleven entries exactly as placed).
Then put the committed files back: `git checkout -- wails-app/frontend/src/wailsjs`, and `git status --short` must show no change under `wailsjs` (remove, one file at a time, any file the generator added). If the diff did show `Task` lines, now edit those entries by hand to the generator's spelling, run Step 5 again and commit them as `feat(tasks): Wails bindings as the generator writes them`. If `wails generate module` fails, say so in your report and go on: the release build regenerates the bindings anyway.

