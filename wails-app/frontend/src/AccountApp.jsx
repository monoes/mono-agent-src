// The root of the app: the monoes.me account in front of the shell (spec §6.5),
// then the first-run onboarding or the app (onboarding/OnboardingGate.jsx).
// Its own file, so it can be tested without mounting every page the shell has.
import { useState } from 'react'
import OnboardingGate from './onboarding/OnboardingGate.jsx'
import AccountShell from './components/account/AccountShell.jsx'
import { createAccountGate } from './lib/accountGate.js'
import { AccountContext } from './lib/accountContext.js'
import { account } from './services/account.js'

export default function AccountApp() {
  const [gate] = useState(() => createAccountGate({ status: account.status }))
  return (
    <AccountContext.Provider value={gate}>
      <AccountShell gate={gate} renderShell={(banners) => <OnboardingGate banners={banners} />} />
    </AccountContext.Provider>
  )
}
