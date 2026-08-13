import { createChromeBypass } from './bypass'
import { registerBackgroundListeners } from './background'

// Service-worker entry (see manifest background.service_worker). The
// background module itself is side-effect free — this file is where the
// worker boots: create the Chrome-backed bypass module (interception install,
// bypass open/close, persisted session state), install the intercept rules,
// then wire the listeners with the module.
const bypass = createChromeBypass()
void bypass.installIntercept()
registerBackgroundListeners(bypass)
