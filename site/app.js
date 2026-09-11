const dialog = document.querySelector("#downloadDialog");
const openers = document.querySelectorAll("[data-open-download]");
const closer = document.querySelector("[data-close-download]");
let lastOpener = null;

function showDownloads(opener = null) {
  lastOpener = opener;
  if (typeof dialog.showModal === "function") {
    dialog.showModal();
  } else {
    dialog.setAttribute("open", "");
  }
  document.body.classList.add("modal-open");
}

function openDownloads(event) {
  showDownloads(event.currentTarget);
}

function closeDownloads() {
  if (typeof dialog.close === "function") {
    dialog.close();
  } else {
    dialog.removeAttribute("open");
  }
}

openers.forEach((button) => button.addEventListener("click", openDownloads));
closer.addEventListener("click", closeDownloads);

dialog.addEventListener("click", (event) => {
  if (event.target === dialog) {
    closeDownloads();
  }
});

dialog.addEventListener("close", () => {
  document.body.classList.remove("modal-open");
  lastOpener?.focus();
});

async function markCurrentPlatform() {
  const agent = navigator.userAgent.toLowerCase();
  let platform = "";
  if (agent.includes("windows")) platform = "windows";
  else if (agent.includes("macintosh") || agent.includes("mac os")) platform = "mac";
  else if (agent.includes("linux")) platform = "linux";
  if (!platform) return;

  const candidates = [...document.querySelectorAll(`[data-platform="${platform}"]`)];
  if (candidates.length === 1) {
    candidates[0].classList.add("recommended");
    return;
  }

  let architecture = "";
  try {
    if (navigator.userAgentData?.getHighEntropyValues) {
      ({ architecture = "" } = await navigator.userAgentData.getHighEntropyValues(["architecture"]));
    }
  } catch {
    // Platform highlighting is optional; download links work without it.
  }
  const wantsArm = /arm|aarch/.test(architecture) || /arm|aarch/.test(agent);
  const needle = wantsArm ? "arm64" : "amd64";
  const exact = candidates.find((card) => card.querySelector(`[data-asset-target$="${needle}"]`));
  (exact || candidates[0])?.classList.add("recommended");
}

function matchingAsset(assets, kind, target, checksum) {
  const prefix = `QDAY-${kind}-`;
  const targetMarker = `-mainnet-${target}.`;
  return assets.find((asset) => {
    const matchesFile = asset.name.startsWith(prefix) && asset.name.includes(targetMarker);
    return matchesFile && (checksum ? asset.name.endsWith(".sha256") : !asset.name.endsWith(".sha256"));
  });
}

async function loadLatestRelease() {
  try {
    const response = await fetch("https://api.github.com/repos/petoshi/qday/releases/latest", {
      headers: { Accept: "application/vnd.github+json" }
    });
    if (!response.ok) return;
    const release = await response.json();
    document.querySelector("#releaseLabel").textContent = `${release.tag_name} // MAINNET`;

    document.querySelectorAll("[data-asset-kind]").forEach((link) => {
      const asset = matchingAsset(release.assets, link.dataset.assetKind, link.dataset.assetTarget, false);
      if (asset) link.href = asset.browser_download_url;
    });
    document.querySelectorAll("[data-checksum-kind]").forEach((link) => {
      const asset = matchingAsset(release.assets, link.dataset.checksumKind, link.dataset.checksumTarget, true);
      if (asset) link.href = asset.browser_download_url;
    });
  } catch {
    // Fixed releases/latest/download links remain available when the API is unreachable.
  }
}

markCurrentPlatform();
loadLatestRelease();

if (window.location.hash === "#download") {
  showDownloads();
}
