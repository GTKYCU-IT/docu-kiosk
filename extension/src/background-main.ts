import { installRules, registerBackgroundListeners } from './background'

// Service-worker entry (see manifest background.service_worker). The
// background module itself is side-effect free — this file is where the
// worker boots: install the interception rules and wire the listeners.
void installRules()
registerBackgroundListeners(chrome)
