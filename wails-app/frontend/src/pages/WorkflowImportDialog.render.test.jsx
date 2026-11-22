// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'

const api = vi.hoisted(() => ({
  importWorkflowFull: vi.fn(),
  chooseWorkflowFile: vi.fn(),
}))
vi.mock('../services/api.js', () => ({ api }))

import ConfirmHost from '../components/ConfirmDialog.jsx'
import { onAutomationsChanged } from '../lib/appEvents.js'
import WorkflowImportDialog, { copyOfExisting, copyReasonOf, splitBundle } from './WorkflowImportDialog.jsx'

afterEach(() => { cleanup(); vi.clearAllMocks() })

async function importFile(res, props = {}) {
  api.chooseWorkflowFile.mockResolvedValue('/w/flow.json')
  api.importWorkflowFull.mockResolvedValueOnce(res)
  render(<><ConfirmHost /><WorkflowImportDialog onClose={() => {}} onOpen={() => {}} {...props} /></>)
  fireEvent.click(screen.getByText('Browse'))
  await waitFor(() => expect(screen.getByLabelText('Workflow file path')).toHaveValue('/w/flow.json'))
  fireEvent.click(screen.getByText('Import'))
}

describe('WorkflowImportDialog', () => {
  it('shows a rejected import and ignores Enter while one is running', async () => {
    api.importWorkflowFull.mockRejectedValueOnce(new Error('cli exploded'))
    render(<WorkflowImportDialog onClose={() => {}} onOpen={() => {}} />)
    const inp = screen.getByLabelText('Workflow file path')
    fireEvent.change(inp, { target: { value: '/w/a.json' } })
    fireEvent.keyDown(inp, { key: 'Enter' })
    fireEvent.keyDown(inp, { key: 'Enter' })
    expect(await screen.findByText(/cli exploded/)).toBeInTheDocument()
    expect(api.importWorkflowFull).toHaveBeenCalledTimes(1)
  })
  it('shows created, updated and unchanged the way the CLI reports them', async () => {
    await importFile({ id: 'w1', name: 'Daily digest', status: 'created' })
    expect(await screen.findByText('Imported “Daily digest”.')).toBeInTheDocument()
    expect(api.importWorkflowFull).toHaveBeenCalledWith('/w/flow.json', { asNew: false })
    cleanup()
    await importFile({ id: 'w1', name: 'Daily digest', status: 'unchanged' })
    expect(await screen.findByText('Already imported — nothing changed.')).toBeInTheDocument()
    cleanup()
    await importFile({ id: 'w1', name: 'Daily digest', status: 'updated' })
    expect(await screen.findByText(/Updated “Daily digest”/)).toBeInTheDocument()
  })

  it('tells the page a workflow was imported (status bar count)', async () => {
    const onImported = vi.fn()
    await importFile({ id: 'w1', name: 'X', status: 'created' }, { onImported })
    await screen.findByText('Imported “X”.')
    expect(onImported).toHaveBeenCalledWith(expect.objectContaining({ id: 'w1' }))
  })

  it('opens the imported workflow', async () => {
    const onOpen = vi.fn()
    await importFile({ id: 'w9', name: 'X', status: 'created' }, { onOpen })
    fireEvent.click(await screen.findByText('Open'))
    expect(onOpen).toHaveBeenCalledWith('w9')
  })

  it('passes --as-new and accepts pasted JSON', async () => {
    api.importWorkflowFull.mockResolvedValue({ id: 'w2', name: 'Copy', status: 'created' })
    render(<WorkflowImportDialog onClose={() => {}} onOpen={() => {}} />)
    fireEvent.change(screen.getByLabelText('Or paste workflow JSON'), { target: { value: '{"name":"Copy","nodes":[]}' } })
    fireEvent.click(screen.getByLabelText(/Import as a new copy/))
    fireEvent.click(screen.getByText('Import'))
    await waitFor(() => expect(api.importWorkflowFull).toHaveBeenCalledWith('{"name":"Copy","nodes":[]}', { asNew: true }))
  })

  it('lists bundled automations and installs missing ones only after a confirm', async () => {
    await importFile({
      id: 'w1', name: 'Scrape', status: 'created',
      automations: [
        { id: 'books-demo', version: '0.1.0', status: 'missing', review: 'Bundled automation books-demo 0.1.0 — publisher Jane — domains books.toscrape.com — steps: navigate', reviewDetail: { id: 'books-demo', version: '0.1.0', publisher: 'Jane', domains: ['books.toscrape.com'], capabilities: ['can open and act on: books.toscrape.com'] } },
        { id: 'hackernews', version: '1.1.0', status: 'present', installedVersion: '1.1.0' },
      ],
      missingAutomations: ['books-demo'], installCommand: 'monoagentcli workflow import --file /w/flow.json --yes',
    })
    expect(await screen.findByText(/needs an automation that is not installed/)).toBeInTheDocument()
    expect(screen.getByText('monoagentcli workflow import --file /w/flow.json --yes')).toBeInTheDocument()
    expect(screen.getAllByText(/publisher Jane/)).toHaveLength(1)
    api.importWorkflowFull.mockResolvedValueOnce({
      id: 'w1', name: 'Scrape', status: 'unchanged',
      automations: [{ id: 'books-demo', version: '0.1.0', status: 'installed' }, { id: 'hackernews', version: '1.1.0', status: 'present' }],
      reviewLines: ['Bundled automation books-demo 0.1.0 — publisher Jane — domains books.toscrape.com — steps: navigate'],
    })
    fireEvent.click(screen.getByText('Install bundled automations'))
    // The confirm lists the package; cancelling installs nothing.
    expect(await screen.findByText('Install bundled automations', { selector: 'div' })).toBeInTheDocument()
    // The confirm shows the dry-run review before anything is installed.
    expect(screen.getByText('Publisher: Jane')).toBeInTheDocument()
    expect(screen.getByText('Domains: books.toscrape.com')).toBeInTheDocument()
    expect(screen.getByText('can open and act on: books.toscrape.com')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Cancel'))
    await waitFor(() => expect(api.importWorkflowFull).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByText('Install bundled automations'))
    fireEvent.click(await screen.findByText('Install'))
    await waitFor(() => expect(api.importWorkflowFull).toHaveBeenLastCalledWith('/w/flow.json', { yes: true }))
    expect(await screen.findByText('installed')).toBeInTheDocument()
    expect(screen.getByText('Installed 1 bundled automation for “Scrape”.')).toBeInTheDocument()
    expect(screen.getByText('Install review')).toBeInTheDocument()
    expect(screen.queryByText('Install bundled automations')).not.toBeInTheDocument()
  })

  it('announces installed automations so the node palette reloads', async () => {
    const onAutomationsInstalled = vi.fn(); const heard = vi.fn()
    const off = onAutomationsChanged(heard)
    await importFile({ id: 'w1', name: 'Scrape', status: 'created', automations: [{ id: 'books-demo', version: '0.1.0', status: 'missing' }], missingAutomations: ['books-demo'] }, { onAutomationsInstalled })
    api.importWorkflowFull.mockResolvedValueOnce({ id: 'w1', name: 'Scrape', status: 'unchanged', automations: [{ id: 'books-demo', version: '0.1.0', status: 'installed' }] })
    fireEvent.click(await screen.findByText('Install bundled automations'))
    fireEvent.click(await screen.findByText('Install'))
    await waitFor(() => expect(onAutomationsInstalled).toHaveBeenCalled())
    expect(heard).toHaveBeenCalledWith({ source: 'workflow-import' })
    off()
  })

  it('shows the trust drop and requires a tick when a bundled package replaces one', async () => {
    await importFile({
      id: 'w1', name: 'Scrape', status: 'created',
      automations: [{ id: 'acme', version: '2.0.0', status: 'missing', reviewDetail: {
        id: 'acme', version: '2.0.0', publisher: 'Jane', domains: ['app.acme.com'],
        capabilities: ["visits profiles — the profile's owner can see your visit (view_profile)"],
        replaces: { id: 'acme', source: 'local', trust: 'recorded', version: '1.0.0' }, replaceRequired: true,
        trustChange: { from: 'recorded', to: 'imported' }, visibility: { profile_view_visible_to_owner: ['view_profile'] } } }],
      missingAutomations: ['acme'],
    })
    expect(await screen.findByText(/Trust drops from recorded to imported/)).toBeInTheDocument()
    expect(screen.getByText(/visits profiles — the profile's owner can see your visit/)).toBeInTheDocument()
    expect(screen.getByText('Replaces local acme 1.0.0')).toBeInTheDocument()
    expect(screen.queryByText('• can open and act on: books.toscrape.com')).not.toBeInTheDocument()
    const install = screen.getByText('Install bundled automations').closest('button')
    expect(install).toBeDisabled()
    fireEvent.click(screen.getByLabelText(/I understand this replaces the local acme/))
    expect(install).not.toBeDisabled()
  })

  it('explains an import kept as a copy in plain words and replaces the existing one on request', async () => {
    await importFile({ id: 'copy-1', name: 'Daily digest', status: 'created',
      warnings: ['a workflow with this name already exists: orig-9; imported as a copy — use --replace orig-9 to replace it'] })
    expect(await screen.findByText(/A workflow named “Daily digest” already exists and was left as it is/)).toBeInTheDocument()
    expect(screen.queryByText(/--replace/)).not.toBeInTheDocument()
    api.importWorkflowFull.mockResolvedValueOnce({ id: 'orig-9', name: 'Daily digest', status: 'updated', removedCopy: 'copy-1' })
    fireEvent.click(screen.getByText('Replace the existing workflow instead'))
    fireEvent.click(await screen.findByText('Replace', { selector: 'button' }))
    await waitFor(() => expect(api.importWorkflowFull).toHaveBeenLastCalledWith('/w/flow.json', { replace: 'orig-9', removeCopy: 'copy-1' }))
    expect(await screen.findByText('Replaced the existing “Daily digest” with this file and removed the copy.')).toBeInTheDocument()
    expect(screen.queryByText('Replace the existing workflow instead')).not.toBeInTheDocument()
  })

  it('uses copyOf and words an edited-copy notice from copyReason', async () => {
    // The CLI's own warning for each reason; the notice stands in for it.
    const warning = {
      id: 'a workflow with this id already exists and was edited locally: orig-7; imported as a copy — use --replace orig-7 to replace it',
      import: 'the workflow imported earlier from this file was edited locally: orig-7; imported as a copy — use --replace orig-7 to replace it',
    }
    for (const reason of ['id', 'import']) {
      await importFile({ id: 'copy-2', name: 'Daily digest', status: 'created', copyOf: 'orig-7', copyReason: reason, warnings: [warning[reason]] })
      expect(await screen.findByText(/your edited version was kept, so this file was imported as a separate copy/)).toBeInTheDocument()
      expect(screen.queryByText(/--replace|edited locally/)).not.toBeInTheDocument()
      api.importWorkflowFull.mockResolvedValueOnce({ id: 'orig-7', name: 'Daily digest', status: 'updated', removedCopy: 'copy-2' })
      fireEvent.click(screen.getByText('Replace the existing workflow instead'))
      fireEvent.click(await screen.findByText('Replace', { selector: 'button' }))
      await waitFor(() => expect(api.importWorkflowFull).toHaveBeenLastCalledWith('/w/flow.json', { replace: 'orig-7', removeCopy: 'copy-2' }))
      cleanup(); vi.clearAllMocks()
    }
  })

  it('keeps the same-name wording for copyReason "name"', async () => {
    await importFile({ id: 'copy-3', name: 'Daily digest', status: 'created', copyOf: 'orig-8', copyReason: 'name', warnings: ['a workflow with this name already exists: orig-8; imported as a copy'] })
    expect(await screen.findByText(/A workflow named “Daily digest” already exists and was left as it is/)).toBeInTheDocument()
  })

  it('shows other warnings as they are', async () => {
    await importFile({ id: 'w1', name: 'X', status: 'created', warnings: ['node "a" uses a deprecated type'] })
    expect(await screen.findByText('⚠ node "a" uses a deprecated type')).toBeInTheDocument()
    expect(screen.queryByText('Replace the existing workflow instead')).not.toBeInTheDocument()
  })

  it('lists packages the file does not carry apart, without counting or installing them', async () => {
    await importFile({
      id: 'w1', name: 'Partial', status: 'created',
      automations: [
        { id: 'shelf-demo', version: '0.1.0', status: 'missing' },
        { id: 'ghost-pkg', version: '2.1.0', status: 'missing', notBundled: true, error: 'not in the bundle: no site domains; install ghost-pkg on this machine first' },
      ],
      missingAutomations: ['shelf-demo', 'ghost-pkg'],
    })
    expect(await screen.findByText('Not included in this file')).toBeInTheDocument()
    expect(screen.getByText('no site domains; install ghost-pkg on this machine first')).toBeInTheDocument()
    expect(screen.getByText(/needs an automation that is not installed\. Its nodes for shelf-demo will not run/)).toBeInTheDocument()
    api.importWorkflowFull.mockResolvedValueOnce({ id: 'w1', name: 'Partial', status: 'unchanged', automations: [
      { id: 'shelf-demo', version: '0.1.0', status: 'installed' },
      { id: 'ghost-pkg', version: '2.1.0', status: 'missing', notBundled: true, error: 'not in the bundle: no site domains; install ghost-pkg on this machine first' },
    ] })
    fireEvent.click(screen.getByText('Install bundled automations'))
    fireEvent.click(await screen.findByText('Install'))
    expect(await screen.findByText(/Installed 1 bundled automation/)).toBeInTheDocument()
    expect(screen.queryByText('Install bundled automations')).not.toBeInTheDocument()
    expect(screen.getByText('Not included in this file')).toBeInTheDocument()
  })

  it('has no install button when only not-bundled packages are missing', async () => {
    await importFile({ id: 'w1', name: 'P', status: 'created', automations: [{ id: 'ghost-pkg', version: '2.1.0', status: 'missing', notBundled: true, error: 'not in the bundle: x' }] })
    expect(await screen.findByText('Not included in this file')).toBeInTheDocument()
    expect(screen.queryByText('Install bundled automations')).not.toBeInTheDocument()
  })

  it('shows differing packages with their changes and replaces them only after a confirm', async () => {
    const differs = { id: 'shelf-demo', version: '0.1.0', status: 'differs', installedVersion: '0.1.0',
      error: 'same version, different content: adds domains cdn.toscrape.com; the installed copy was kept. To replace it, re-import with --replace-automations',
      changes: { addedDomains: ['cdn.toscrape.com'], addedScripts: ['grab.js'] },
      reviewDetail: { id: 'shelf-demo', version: '0.1.0', domains: [], capabilities: [], replaces: { id: 'shelf-demo', source: 'imported', trust: 'imported', version: '0.1.0' }, replaceRequired: false, visibility: {} } }
    await importFile({ id: 'w1', name: 'Mixed', status: 'created', automations: [differs] })
    expect(await screen.findByText('Different from what is installed')).toBeInTheDocument()
    expect(screen.getByText('New sites it may open: cdn.toscrape.com')).toBeInTheDocument()
    expect(screen.getByText('New page scripts: grab.js')).toBeInTheDocument()
    expect(screen.queryByText(/--replace-automations/)).not.toBeInTheDocument()
    expect(screen.queryByText('Install bundled automations')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText("Replace with the file's version"))
    // Confirm lists the changes; cancelling replaces nothing.
    expect(await screen.findAllByText('New sites it may open: cdn.toscrape.com')).toHaveLength(2)
    fireEvent.click(screen.getByText('Cancel'))
    await waitFor(() => expect(api.importWorkflowFull).toHaveBeenCalledTimes(1))
    api.importWorkflowFull.mockResolvedValueOnce({ id: 'w1', name: 'Mixed', status: 'unchanged', automations: [{ ...differs, status: 'replaced', error: '' }] })
    fireEvent.click(screen.getByText("Replace with the file's version"))
    fireEvent.click(await screen.findByText('Replace', { selector: 'button' }))
    await waitFor(() => expect(api.importWorkflowFull).toHaveBeenLastCalledWith('/w/flow.json', { yes: true, replaceAutomations: true }))
    expect(await screen.findByText("Replaced 1 installed automation with the file's version.")).toBeInTheDocument()
    expect(screen.queryByText("Replace with the file's version")).not.toBeInTheDocument()
  })

  it('never offers to replace a built-in from a file', async () => {
    await importFile({ id: 'w1', name: 'M', status: 'created', automations: [{ id: 'hackernews', version: '1.1.0', status: 'differs', changes: { addedDomains: ['x.example.com'] },
      reviewDetail: { replaces: { id: 'hackernews', source: 'builtin', trust: 'builtin', version: '1.1.0' } } }] })
    expect(await screen.findByText('The installed copy is a built-in; a workflow file never replaces it.')).toBeInTheDocument()
    expect(screen.queryByText("Replace with the file's version")).not.toBeInTheDocument()
  })

  it('says a local-only package works only on the sender machine', async () => {
    await importFile({ id: 'w1', name: 'M', status: 'created', automations: [{ id: 'local-tool', version: '0.1.0', status: 'missing', notBundled: true, localOnly: true, error: 'not in the bundle: opens a local address (localhost:8080)' }] })
    expect(await screen.findByText("Only works on the sender's machine — recreate it here or ask the sender.")).toBeInTheDocument()
  })

  it('uses builtin and replaceable from the CLI and never renders its hint', async () => {
    await importFile({ id: 'w1', name: 'M', status: 'created', automations: [
      { id: 'hackernews', version: '1.1.0', status: 'differs', builtin: true, replaceable: false, changes: { addedDomains: ['x.example.com'] },
        hint: 'A bundle never replaces a built-in; the installed copy stays.' },
      { id: 'locked', version: '1.0.0', status: 'differs', replaceable: false, changes: {} },
      { id: 'shelf-demo', version: '0.1.0', status: 'differs', replaceable: true, changes: { addedDomains: ['cdn.toscrape.com'] },
        hint: 'Re-import with --replace-automations to review and replace it.' },
    ] })
    expect(await screen.findByText('The installed copy is a built-in; a workflow file never replaces it.')).toBeInTheDocument()
    expect(screen.getByText('This file cannot replace the installed copy.')).toBeInTheDocument()
    expect(screen.queryByText(/bundle never replaces|--replace-automations/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByText("Replace with the file's version"))
    expect(await screen.findByText(/Replace the installed copy with the file's version/)).toBeInTheDocument()
  })

  it('words not-bundled advice itself instead of the CLI hint', async () => {
    await importFile({ id: 'w1', name: 'M', status: 'created', automations: [
      { id: 'ghost-pkg', version: '2.1.0', status: 'missing', notBundled: true, hint: 'Ask the sender to re-run workflow export with --include-automations.',
        error: 'not in the bundle: no signed copy; the sender can re-run workflow export with --include-automations' },
      { id: 'local-tool', version: '0.1.0', status: 'missing', notBundled: true, localOnly: true, hint: 'Recreate local-tool on this machine, or ask the sender for it.',
        error: 'not in the bundle: opens a local address (localhost:8080)' },
    ] })
    expect(await screen.findByText('no signed copy')).toBeInTheDocument()
    expect(screen.getByText('Install it here some other way, or ask the sender to include it in the file.')).toBeInTheDocument()
    expect(screen.getByText("Only works on the sender's machine — recreate it here or ask the sender.")).toBeInTheDocument()
    expect(screen.queryByText(/--include-automations|Recreate local-tool/)).not.toBeInTheDocument()
  })

  it('shows CLI errors inline and closes on Escape', async () => {
    const onClose = vi.fn()
    api.importWorkflowFull.mockResolvedValue({ error: 'node "x" has no type' })
    render(<WorkflowImportDialog onClose={onClose} onOpen={() => {}} />)
    fireEvent.change(screen.getByLabelText('Workflow file path'), { target: { value: '/w/bad.json' } })
    fireEvent.click(screen.getByText('Import'))
    expect(await screen.findByRole('alert')).toHaveTextContent('node "x" has no type')
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })
})

describe('import result helpers', () => {
  it('copyReasonOf maps id/import to edited and defaults to name', () => {
    expect(copyReasonOf({ copyReason: 'id' })).toBe('edited')
    expect(copyReasonOf({ copyReason: 'import' })).toBe('edited')
    expect(copyReasonOf({ copyReason: 'name' })).toBe('name')
    expect(copyReasonOf({})).toBe('name')
  })

  it('copyOfExisting prefers a structured field and reads the copy warning', () => {
    expect(copyOfExisting({ copyOf: 'a1', warnings: ['a workflow with this name already exists: b2; imported as a copy'] })).toBe('a1')
    expect(copyOfExisting({ warnings: ['a workflow with this name already exists: b2; imported as a copy — use --replace b2'] })).toBe('b2')
    expect(copyOfExisting({ warnings: ['a workflow with this id already exists and was edited locally: wf-d9; imported as a copy — use --replace wf-d9 to replace it'] })).toBe('wf-d9')
    expect(copyOfExisting({ warnings: ['the workflow imported earlier from this file was edited locally: 7672; imported as a copy — use --replace 7672 to replace it'] })).toBe('7672')
    expect(copyOfExisting({ warnings: ['something else'] })).toBe(null)
  })
  it('splitBundle keeps not-bundled packages out of the installable set', () => {
    const r = splitBundle([{ id: 'a', status: 'missing' }, { id: 'b', status: 'missing', notBundled: true }, { id: 'c', status: 'present', notBundled: true }])
    expect(r.installable.map(i => i.id)).toEqual(['a'])
    expect(r.notIncluded.map(i => i.id)).toEqual(['b'])
    expect(r.listed.map(i => i.id)).toEqual(['a', 'c'])
  })
  it('splitBundle takes replaceable from the CLI, else leaves out built-ins', () => {
    const r = splitBundle([
      { id: 'a', status: 'differs', replaceable: true },
      { id: 'b', status: 'differs', builtin: true, replaceable: false },
      { id: 'c', status: 'differs', replaceable: false },
      { id: 'd', status: 'differs', reviewDetail: { replaces: { source: 'builtin' } } },
      { id: 'e', status: 'differs' },
    ])
    expect(r.differs.map(i => i.id)).toEqual(['a', 'b', 'c', 'd', 'e'])
    expect(r.replaceable.map(i => i.id)).toEqual(['a', 'e'])
  })
})
