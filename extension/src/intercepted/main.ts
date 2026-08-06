import '../app.css'
import { mount } from 'svelte'
import { Toaster } from '$lib/components/ui/sonner'

import App from './App.svelte'
mount(Toaster, { target: document.body })
mount(App, { target: document.body })
