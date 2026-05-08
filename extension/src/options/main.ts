type Config = {
  kioskId: string;
  brokerUrl: string;
};

const kioskInput = document.getElementById("kioskId") as HTMLInputElement;
const brokerInput = document.getElementById("brokerUrl") as HTMLInputElement;
const saveBtn = document.getElementById("save") as HTMLButtonElement;

async function load() {
  const data = await chrome.storage.local.get([
    "kioskId",
    "brokerUrl"
  ]) as Partial<Config>;

  kioskInput.value = data.kioskId ?? "";
  brokerInput.value = data.brokerUrl ?? "";
}

async function save() {
  const config: Config = {
    kioskId: kioskInput.value,
    brokerUrl: brokerInput.value
  };

  await chrome.storage.local.set(config);

  alert("Saved");
}

saveBtn.addEventListener("click", save)

load();
