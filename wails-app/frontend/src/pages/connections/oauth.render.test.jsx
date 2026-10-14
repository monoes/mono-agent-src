// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'

const events = vi.hoisted(() => ({ progress: null, done: null }))
const api = vi.hoisted(() => ({
  showPlatform: vi.fn(),
  connectPlatformOAuth: vi.fn(() => Promise.resolve('started')),
  cancelPlatformOAuth: vi.fn(() => Promise.resolve('ok')),
  getOAuthCredentials: vi.fn(() => Promise.resolve('')),
  setOAuthCredentials: vi.fn(),
  saveConnectionDirect: vi.fn(),
  openURL: vi.fn(),
}))
vi.mock('../../services/api.js', () => ({
  api,
  onConnectionProgress: (cb) => { events.progress = cb; return () => {} },
  onConnectionDone: (cb) => { events.done = cb; return () => {} },
}))

import ApiConnectionModal from './ApiConnectionModal.jsx'
import ApiConnections from './ApiConnections.jsx'
import { oauthFormFields, oauthFieldArgs, missingRequired } from './OAuthFields.jsx'

// jsdom has no scrollIntoView; the progress list scrolls to its last step.
Element.prototype.scrollIntoView = () => {}

afterEach(() => { cleanup(); vi.clearAllMocks() })

// `connect show generic_oauth1 --json`, trimmed.
const genericOAuth1 = {
  id: 'generic_oauth1', name: 'Generic OAuth 1.0a', methods: ['oauth1', 'apikey'],
  fields: { oauth1: [
    { key: 'base_url', label: 'Base URL', help_text: "Completes the HTTP Request node's path-only URLs" },
    { key: 'request_token_url', label: 'Request Token URL', required: true },
    { key: 'authorization_url', label: 'Authorization URL', required: true },
    { key: 'access_token_url', label: 'Access Token URL', required: true },
    { key: 'consumer_key', label: 'Consumer Key', required: true },
    { key: 'consumer_secret', label: 'Consumer Secret', secret: true, required: true },
    { key: 'signature_method', label: 'Signature Method' },
  ] },
  oauth1: {},
}

// `connect show microsoft_dynamics_oauth --json`, trimmed.
const dynamics = {
  id: 'microsoft_dynamics_oauth', name: 'Microsoft Dynamics CRM', methods: ['oauth'],
  oauth: { auth_url: 'https://login.microsoftonline.com/common/oauth2/v2.0/authorize', token_url: 'x', per_tenant: true,
    connect_fields: [
      { key: 'subdomain', label: 'Subdomain', required: true, help_text: 'The part before .crm.dynamics.com' },
      { key: 'region', label: 'Region' },
    ] },
}

const platformOf = (v) => ({ id: v.id, name: v.name, methods: v.methods, fields: {} })

function renderModal(view) {
  api.showPlatform.mockResolvedValue(view)
  return render(<ApiConnectionModal platform={platformOf(view)} conn={null} onClose={() => {}} onRefresh={() => Promise.resolve()} onDisconnect={() => {}} />)
}

describe('argument building', () => {
  it('turns non-secret values into --field pairs and secret ones into stdin JSON', () => {
    const fields = oauthFormFields(genericOAuth1, 'oauth1')
    const { pairs, secretJSON } = oauthFieldArgs(fields, {
      request_token_url: ' http://127.0.0.1:9/rt ', consumer_key: 'ck', consumer_secret: 's3cret', signature_method: '',
    })
    expect(pairs).toEqual(['request_token_url=http://127.0.0.1:9/rt', 'consumer_key=ck'])
    expect(pairs.join(' ')).not.toContain('s3cret')
    expect(JSON.parse(secretJSON)).toEqual({ consumer_secret: 's3cret' })
  })

  it('sends no stdin when there is no secret, and reads tenant connect fields for oauth', () => {
    const fields = oauthFormFields(dynamics, 'oauth')
    expect(fields.map(f => f.key)).toEqual(['subdomain', 'region'])
    expect(oauthFieldArgs(fields, { subdomain: 'acme' })).toEqual({ pairs: ['subdomain=acme'], secretJSON: '' })
    expect(missingRequired(fields, {})).toEqual(['subdomain'])
    expect(oauthFormFields(null, 'oauth1')).toEqual([])
    expect(oauthFormFields(dynamics, 'apikey')).toEqual([])
  })
})

describe('OAuth 1.0a platform', () => {
  it('renders the oauth1 form from connect show, secrets masked, and starts the flow with --field pairs', async () => {
    const log = vi.spyOn(console, 'log').mockImplementation(() => {})
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    renderModal(genericOAuth1)
    expect(await screen.findByLabelText(/Request Token URL/)).toHaveAttribute('type', 'text')
    expect(screen.getByLabelText(/Consumer Secret/)).toHaveAttribute('type', 'password')
    expect(screen.getByRole('button', { name: 'oauth1' })).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('Client ID')).not.toBeInTheDocument()

    const connect = screen.getByRole('button', { name: /Connect with Generic OAuth 1.0a/ })
    expect(connect).toBeDisabled()
    const fill = { 'Request Token URL': 'http://127.0.0.1:9/rt', 'Authorization URL': 'http://127.0.0.1:9/auth', 'Access Token URL': 'http://127.0.0.1:9/at', 'Consumer Key': 'ck', 'Consumer Secret': 'top-secret' }
    for (const [lbl, v] of Object.entries(fill)) fireEvent.change(screen.getByLabelText(new RegExp(lbl)), { target: { value: v } })
    expect(connect).toBeEnabled()
    fireEvent.click(connect)

    await waitFor(() => expect(api.connectPlatformOAuth).toHaveBeenCalledTimes(1))
    expect(api.connectPlatformOAuth).toHaveBeenCalledWith('generic_oauth1',
      ['request_token_url=http://127.0.0.1:9/rt', 'authorization_url=http://127.0.0.1:9/auth', 'access_token_url=http://127.0.0.1:9/at', 'consumer_key=ck'],
      '{"consumer_secret":"top-secret"}')

    // Running: progress streams in and the flow can be cancelled.
    await act(async () => { events.progress({ platform: 'generic_oauth1', message: 'Waiting for authorization in your browser…', kind: 'info' }) })
    expect(screen.getByText('Waiting for authorization in your browser…')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Cancel/ }))
    expect(api.cancelPlatformOAuth).toHaveBeenCalledWith('generic_oauth1')
    await act(async () => { events.done({ platform: 'generic_oauth1', success: false, cancelled: true, error: 'Cancelled' }) })
    expect(screen.getByRole('button', { name: /Connect with/ })).toBeInTheDocument()

    const logged = [...log.mock.calls, ...err.mock.calls].flat().map(String).join('\n')
    expect(logged).not.toContain('top-secret')
    log.mockRestore(); err.mockRestore()
  })

  it('shows the CLI error and offers a retry', async () => {
    renderModal(genericOAuth1)
    await screen.findByLabelText(/Consumer Key/)
    for (const [lbl, v] of [['Request Token URL', 'a'], ['Authorization URL', 'b'], ['Access Token URL', 'c'], ['Consumer Key', 'd'], ['Consumer Secret', 'e']]) {
      fireEvent.change(screen.getByLabelText(new RegExp(lbl)), { target: { value: v } })
    }
    fireEvent.click(screen.getByRole('button', { name: /Connect with/ }))
    await waitFor(() => expect(api.connectPlatformOAuth).toHaveBeenCalled())
    await act(async () => { events.done({ platform: 'generic_oauth1', success: false, error: 'oauth1 request token: 401 Unauthorized' }) })
    expect(screen.getByText('oauth1 request token: 401 Unauthorized')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Retry/ })).toBeEnabled()
  })
})

describe('tenant connect fields', () => {
  it('asks for the tenant values before starting OAuth and passes them as --field', async () => {
    api.getOAuthCredentials.mockResolvedValue('{"clientID":"cid","clientSecret":""}')
    renderModal(dynamics)
    const sub = await screen.findByLabelText(/Subdomain/)
    expect(screen.getByText('The part before .crm.dynamics.com')).toBeInTheDocument()
    expect(await screen.findByDisplayValue('cid')).toBeInTheDocument()

    const connect = screen.getByRole('button', { name: /Connect with Microsoft Dynamics CRM/ })
    expect(connect).toBeDisabled()
    expect(screen.getByText('Fill in the required fields above first')).toBeInTheDocument()
    fireEvent.change(sub, { target: { value: 'acme' } })
    expect(connect).toBeEnabled()
    fireEvent.click(connect)
    await waitFor(() => expect(api.connectPlatformOAuth).toHaveBeenCalledWith('microsoft_dynamics_oauth', ['subdomain=acme'], ''))
  })

  it('needs no stored OAuth client when client_id is a connect field', async () => {
    renderModal({ id: 'strava', name: 'Strava', methods: ['oauth'], oauth: { connect_fields: [
      { key: 'client_id', label: 'Client ID', required: true },
      { key: 'client_secret', label: 'Client Secret', secret: true, required: true },
    ] } })
    await screen.findByLabelText(/Client Secret/)
    expect(screen.queryByText('OAuth App Credentials')).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/Client ID/), { target: { value: 'id1' } })
    fireEvent.change(screen.getByLabelText(/Client Secret/), { target: { value: 'sec1' } })
    fireEvent.click(screen.getByRole('button', { name: /Connect with Strava/ }))
    await waitFor(() => expect(api.connectPlatformOAuth).toHaveBeenCalledWith('strava', ['client_id=id1'], '{"client_secret":"sec1"}'))
  })
})

describe('platform list', () => {
  it('shows app-category platforms under Apps and filters by name or id', () => {
    const platforms = [
      { id: 'trello_oauth1', name: 'Trello (OAuth 1.0a)', category: 'app', methods: ['oauth1'] },
      { id: 'microsoft_dynamics_oauth', name: 'Microsoft Dynamics 365 (sign in)', category: 'app', methods: ['oauth'] },
      { id: 'generic_oauth1', name: 'Generic OAuth 1.0a', category: 'custom', methods: ['oauth1', 'apikey'] },
    ]
    const onSelect = vi.fn()
    render(<ApiConnections platforms={platforms} connections={[]} onSelect={onSelect} />)
    expect(screen.getByText('Apps')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Filter API platforms'), { target: { value: 'oauth1' } })
    expect(screen.getByText('Trello (OAuth 1.0a)')).toBeInTheDocument()
    expect(screen.getByText('Generic OAuth 1.0a')).toBeInTheDocument()
    expect(screen.queryByText('Microsoft Dynamics 365 (sign in)')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('Trello (OAuth 1.0a)'))
    expect(onSelect).toHaveBeenCalledWith(platforms[0])
  })
})
