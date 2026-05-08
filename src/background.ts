import { getConfig } from "./config";

// function extractCode(url: string): string | null {
//   try {
//     const parsed = new URL(url);
//     return parsed.searchParams.get("code");
//   } catch {
//     return null;
//   }
// }
//
// function isDocuSignStart(url: string): boolean {
//   return url.includes("/Signing/StartInSession.aspx")
//     && url.includes("code=")
// }
//
// function sendToBroker(payload: {
//   kioskId: string;
//   docusignCode: string;
//   sourceUrl: string;
// }) {
//   console.log("PAYLOAD:", payload)
//   // try {
//   //   await fetch(CONFIG.BROKER_URL, {
//   //     method: "POST",
//   //     headers: {
//   //       "Content-Type": "application/json"
//   //     },
//   //     body: JSON.stringify(payload)
//   //   });
//   // } catch (err) {
//   //   console.error("Broker call failed:", err);
//   // }
// }

function captureSigningUrl(details: chrome.webRequest.OnBeforeRequestDetails): string {
  let url = details.url;

  if (details.method === "POST" && details.requestBody?.formData) {
    try {
      const formData = details.requestBody.formData;
      const params = new URLSearchParams();

      for (const [key, values] of Object.entries(formData)) {
        if (values && values.length > 0) {
          params.append(key, values[0].toString());
        }
      }

      url = `${url}?${params.toString()}`;
    } catch (e) {
      console.error("Could not parse request body", e)
    }
  }

  return url;
}

console.log("DocuSign interceptor loaded");
chrome.webRequest.onBeforeRequest.addListener((details) => {
  // const { url, method, requestBody } = details;
  const config = getConfig();

  console.log("Kiosk ID:", config.kioskId);
  console.log("Broker URL", config.brokerUrl);


  // if (!url || !isDocuSignStart(url)) {
  //   return undefined;
  // }

  // const code = extractCode(url);
  // console.log("Extracted code:", code)
  //
  // if (!code) {
  //   return undefined;
  // }

  // sendToBroker({
  //   kioskId: config.kioskId,
  //   docusignCode: code,
  //   sourceUrl: url
  // });


  const signingUrl = captureSigningUrl(details);
  console.log("Signing URL:", signingUrl);

  return { cancel: true }
}, {
  urls: [
    "*://*.docusign.net/*",
    "*://*.docusign.com/*",
  ]
},
  ["requestBody"]);
