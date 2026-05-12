const brokerInput = document.getElementById('brokerUrl') as HTMLInputElement
const saveBtn = document.getElementById('save') as HTMLButtonElement

async function load() {
  const data = await chrome.storage.local.get('brokerUrl') as { brokerUrl?: string }
  brokerInput.value = data.brokerUrl ?? ''
}

async function save() {
  await chrome.storage.local.set({ brokerUrl: brokerInput.value })
  alert('Saved')
}

saveBtn.addEventListener('click', save)
load()
