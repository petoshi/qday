"use strict";
const $ = id => document.getElementById(id);
let token = sessionStorage.getItem("qdayToken") || "";
let latest = null, backupPending = false, busy = false, stopped = false, connected = false;
let transferDraft = null, messageTimer, polling = null, chosenThreads = false;
let setupMode = "create", setupDismissed = false, proofDraft = null, burnDraft = null;
let backupMustSave = false, importAddress = "", lockedRecoveryAddress = "";
let balanceSnapshot = null;
let lastNodeError = "";
let lastConnectionError = "";
let lockRequested = false;
let feeScope = "";
const samples = [];
// One-use launch codes disappear before the first request; the long-lived
// node credential is never part of an automatic browser URL.
const launchCode = new URLSearchParams(location.hash.slice(1)).get("launch");
if (location.hash) history.replaceState(null, "", location.pathname);
function coins(value) {
  const [whole, fraction] = String(value).split(".");
  return whole.replace(/\B(?=(\d{3})+(?!\d))/g, " ") + (fraction === undefined ? "" : "." + fraction);
}
function duration(seconds) {
  seconds = Math.max(0, Math.ceil(Number(seconds) || 0));
  const days = Math.floor(seconds / 86400); seconds %= 86400;
  const hours = Math.floor(seconds / 3600); seconds %= 3600;
  const minutes = Math.floor(seconds / 60); seconds %= 60;
  return [[days,"d"],[hours,"h"],[minutes,"m"],[seconds,"s"]].filter(([value], index) => value || index === 3).slice(0, 3).map(([value, unit]) => value + unit).join(" ");
}
function genesisTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value || "the scheduled genesis time");
  return new Intl.DateTimeFormat(undefined, {dateStyle:"medium",timeStyle:"long"}).format(date);
}
function message(text = "", error = false, persistent = false) {
  clearTimeout(messageTimer);
  $("messageText").textContent = text;
  $("message").hidden = !text;
  $("message").classList.toggle("error", error);
  document.querySelectorAll("dialog .dialog-message").forEach(el => {el.textContent = "";});
  const modal = Array.from(document.querySelectorAll("dialog[open]")).at(-1);
  const local = modal?.querySelector("form:not([hidden]) .dialog-message") || modal?.querySelector(".dialog-message");
  if (local) {local.textContent = text;local.classList.toggle("error", error);}
  if (text && !persistent && !error) messageTimer = setTimeout(() => message(), 10000);
}
$("dismissMessage").onclick = () => message();
function view(name, focus) {
  if (latest?.hasWallet && !latest.unlocked) return;
  if (!["overview", "send", "receive", "survival", "settings"].includes(name)) return;
  document.querySelectorAll(".view").forEach(el => { el.hidden = el.id !== "view-" + name; });
  document.querySelectorAll(".nav-item").forEach(el => {
    const active = el.dataset.view === name && (el.dataset.focus || "") === (focus || "");
    el.classList.toggle("active", active);
    if (active) el.setAttribute("aria-current", "page"); else el.removeAttribute("aria-current");
  });
  $("pageName").textContent = focus === "miner" ? "CPU mining" : name[0].toUpperCase() + name.slice(1);
  if (focus) $(focus).scrollIntoView({behavior: "smooth", block: "center"});
  else window.scrollTo({top: 0, behavior: "instant"});
}
document.querySelectorAll("[data-view]").forEach(el => {
  el.addEventListener("click", () => view(el.dataset.view, el.dataset.focus));
});
async function api(path, body) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), body === undefined ? 15000 : 120000);
  try {
    const res = await fetch("/api/" + path, {
      method: body === undefined ? "GET" : "POST",
      headers: {Authorization: "Bearer " + token, "Content-Type": "application/json"},
      body: body === undefined ? undefined : JSON.stringify(body),
      cache: "no-store", signal: controller.signal
    });
    if (res.status === 401) throw new Error("Open QDAY Wallet again to reconnect this browser tab.");
    let data;
    try { data = await res.json(); } catch { throw new Error("Local node returned HTTP " + res.status); }
    if (!res.ok) throw new Error(data.error || "Request failed");
    return data;
  } catch (error) {
    if (controller.signal.aborted) throw new Error(body === undefined
      ? "Local node is not responding. Reconnecting…"
      : "The node did not respond in time. Check the wallet status before trying this action again.");
    throw error;
  } finally { clearTimeout(timer); }
}
function atomicAmount(value, unit, label = "Amount") {
  const decimals = String(unit).length - 1;
  if (!/^(0|[1-9]\d*)(\.\d+)?$/.test(value) || value.length > 80) throw new Error(label + " must be a non-negative decimal amount in QDAY.");
  const [whole, fraction = ""] = value.split(".");
  if (fraction.length > decimals) throw new Error(label + " has too many decimal places.");
  const amount = BigInt(whole) * BigInt(unit) + BigInt(fraction.padEnd(decimals, "0") || "0");
  if (amount > (1n << 128n) - 1n) throw new Error(label + " is too large.");
  return amount;
}
function decimalAmount(amount, unit) {
  const scale = BigInt(unit), decimals = String(unit).length - 1;
  const fraction = (amount % scale).toString().padStart(decimals, "0").replace(/0+$/, "");
  return (amount / scale).toString() + (fraction ? "." + fraction : "");
}
function setFeeMode(field, mode) {
  field.dataset.mode = mode;
  field.querySelectorAll("[data-fee-mode]").forEach(button => button.setAttribute("aria-pressed", String(button.dataset.feeMode === mode)));
  field.querySelector("[data-fee-custom]").hidden = mode !== "custom";
  const input = field.querySelector("[data-fee-input]");
  input.required = mode === "custom";
  input.disabled = mode !== "custom" || busy || !connected;
}
document.querySelectorAll("[data-fee]").forEach(field => {
  field.querySelectorAll("[data-fee-mode]").forEach(button => {
    button.onclick = () => {
      setFeeMode(field, button.dataset.feeMode);
      if (field.dataset.mode === "custom") field.querySelector("[data-fee-input]").focus();
    };
  });
});
function renderFees(s) {
  const scope = (s.address || "") + ":" + s.unit;
  document.querySelectorAll("[data-fee]").forEach(field => {
    const standard = field.dataset.fee === "proof" ? s.proofFee : s.fee;
    if (scope !== feeScope) {
      field.querySelector("[data-fee-input]").value = "";
      setFeeMode(field, "auto");
    }
    field.querySelector("[data-fee-auto]").textContent = coins(standard) + " QDAY";
    field.querySelector("[data-fee-input]").placeholder = standard;
    field.querySelector("[data-fee-note]").textContent = field.dataset.fee === "proof"
      ? "Publication requires at least " + coins(standard) + " QDAY."
      : "Auto uses the standard wallet fee. Custom sets the total fee paid to the miner.";
  });
  feeScope = scope;
}
function selectedFee(name, standard = latest.fee, unit = latest.unit) {
  const field = document.querySelector('[data-fee="' + name + '"]');
  const fee = field.dataset.mode === "custom" ? field.querySelector("[data-fee-input]").value.trim() : standard;
  const atomic = atomicAmount(fee, unit, "Fee");
  if (name === "proof" && atomic < atomicAmount(standard, unit)) throw new Error("Publication fee must be at least " + coins(standard) + " QDAY.");
  return fee;
}
function resetFee(name) {
  const field = document.querySelector('[data-fee="' + name + '"]');
  field.querySelector("[data-fee-input]").value = "";
  setFeeMode(field, "auto");
}
function reviewPayment(name, amount) {
  const fee = selectedFee(name);
  const total = atomicAmount(amount, latest.unit) + atomicAmount(fee, latest.unit, "Fee");
  if (total > atomicAmount(latest.balance, latest.unit)) throw new Error("Amount and fee exceed your available balance.");
  return {fee, total:decimalAmount(total, latest.unit)};
}
function controls() {
  $("unlockConnection").hidden = connected;
  document.querySelectorAll("[data-fee]").forEach(field => {
    field.querySelectorAll("[data-fee-mode]").forEach(button => {button.disabled = busy || !connected;});
    field.querySelector("[data-fee-input]").disabled = field.dataset.mode !== "custom" || busy || !connected;
  });
  const ready = connected && latest?.unlocked && latest?.synced && latest?.balanceReady && !backupPending && !busy && !stopped;
  $("start").disabled = !connected || !latest?.unlocked || !latest?.networkSynced || latest?.genesisReady === false || backupPending || busy || stopped;
  $("send").disabled = !ready;
  $("confirmTransfer").disabled = !ready;
  $("reviewBurn").disabled = !ready;
  $("confirmBurn").disabled = !ready || !burnDraft || $("burnConfirmation").value.trim() !== "BURN";
  $("stop").disabled = !connected || latest?.mode === "STOP" || stopped;
  $("lock").disabled = !connected || busy || stopped;
  $("quit").disabled = !connected || busy || stopped;
  $("quitLocked").disabled = $("quit").disabled;
  $("recoverLocked").disabled = !connected || busy || stopped;
  $("backToUnlock").disabled = !connected || busy || stopped;
  $("openRecovery").disabled = !connected || busy || stopped || !latest?.hasWallet;
  $("openImport").disabled = !connected || busy || stopped;
  $("addPeer").disabled = !connected || busy || stopped;
  $("peerAddress").disabled = !connected || busy || stopped;
  $("restart").disabled = !connected || busy || stopped || !latest?.canRestart;
  $("confirmRestart").disabled = $("restart").disabled;
  $("cancelRestart").disabled = busy;
  $("restartNote").textContent = latest?.canRestart ? "Restart the local node and reopen the wallet. Mining and DEFEND stop; unlock your wallet again to resume." : "For standalone nodes, use the process manager to restart. QDAY Wallet provides automatic restarts when launched from the desktop app.";
  const available = connected && !stopped;
  const unlocked = available && !!latest?.unlocked;
  const needsWallet = available && latest && !latest.hasWallet;
  const lockLabel = !available ? "Local node unavailable" : needsWallet ? "Set up wallet" : unlocked ? "Wallet unlocked. Lock wallet and stop CPU activity" : "Wallet locked. Unlock wallet";
  $("lock").title = lockLabel; $("lock").setAttribute("aria-label", lockLabel);
  $("lock").setAttribute("aria-controls", needsWallet ? "setup" : unlocked ? "lockPrompt" : "unlock");
  $("lockText").textContent = !available ? (latest ? "Offline" : "Connecting…") : needsWallet ? "Set up wallet" : unlocked ? "Unlocked" : "Locked";
  $("lockIcon").setAttribute("href", unlocked ? "#i-unlock" : "#i-lock");
  $("lock").classList.toggle("unlocked", unlocked);
  $("lock").classList.toggle("locked", available && !!latest?.hasWallet && !unlocked);
  $("lock").classList.toggle("wallet-setup-button", !!needsWallet);
  document.querySelector(".topbar").classList.toggle("needs-wallet", !!needsWallet);
  $("walletState").classList.toggle("unlocked", unlocked);
  $("walletStateIcon").setAttribute("href", unlocked ? "#i-unlock" : "#i-lock");
  $("walletStateText").textContent = !available ? "Wallet status unavailable" : needsWallet ? "No wallet yet" : unlocked ? "Wallet unlocked" : "Wallet locked";
  $("confirmLock").disabled = !unlocked || busy;
  $("cancelLock").disabled = busy;
  const activity = latest?.qday ? "DEFEND" : "mining";
  const miningReason = stopped ? "QDAY is stopped. Open the app again to continue."
    : !connected ? "Waiting for the local node to connect."
    : !latest?.hasWallet ? `Create or restore a wallet to start ${activity}.`
    : !latest.unlocked ? `Your wallet is locked. Unlock it to start ${activity}.`
    : backupPending ? (backupMustSave ? "Save your seed phrase and confirm you have saved it to continue." : "Close the seed phrase window to continue.")
    : !latest.networkSynced ? `Waiting for a synchronized network connection before ${activity} can start.`
    : latest.genesisReady === false ? `Mining opens in ${duration(latest.genesisWaitSeconds)} · ${genesisTime(latest.genesisTimestamp)}.`
    : busy ? "Finishing the current wallet action…" : "";
  $("miningNotice").hidden = !miningReason;
  $("miningNoticeText").textContent = miningReason;
  $("miningWalletAction").hidden = !available || !!latest?.unlocked || busy;
  $("miningWalletAction").textContent = needsWallet ? "Set up wallet" : "Unlock wallet";
  $("start").title = miningReason || (latest?.qday ? "Start DEFEND" : "Start CPU mining");
  if (!available) $("balanceNote").textContent = balanceSnapshot ? "Node offline · last known balance" : "Waiting for the local node…";
  $("copyAddress").disabled = !latest?.address;
  $("verifyProof").disabled = !connected || !!latest?.qdayHeight || !!latest?.proofPending || busy || stopped;
  $("confirmProof").disabled = !ready || !proofDraft || !!latest?.qdayHeight || !!latest?.proofPending;
  $("proofWalletNotice").hidden = !!latest?.unlocked && !!latest?.synced;
  document.querySelectorAll("#createForm button, #unlockForm button, #lockedRecoveryForm button, #recoveryForm button, [data-setup], #browseSetup, #cancelTransfer, #cancelBurn, #cancelProof").forEach(el => {el.disabled = busy || !connected;});
  document.querySelectorAll("#recoveryForm button").forEach(el => {el.disabled = busy || !connected || !latest?.hasWallet;});
  document.querySelectorAll("#importForm button, #cancelImport, #cancelRecovery").forEach(el => {el.disabled = busy || !connected;});
}
function drawHashrate(rate) {
  const now = Date.now();
  if (!samples.length || now - samples[samples.length - 1].time >= 1500) samples.push({time: now, value: Math.max(0, rate)});
  while (samples.length && samples[0].time < now - 60000) samples.shift();
  const maximum = Math.max(1, ...samples.map(s => s.value));
  const points = samples.map(s => [(600 * (s.time - now + 60000) / 60000).toFixed(2), (55 - s.value / maximum * 44).toFixed(2)]);
  if (!points.length) return;
  // No invented history: only actual native-node samples are drawn.
  const line = points.map(([x,y],i) => (i ? "L" : "M") + x + " " + y).join(" ");
  $("hashLine").setAttribute("d", line);
  $("hashArea").setAttribute("d", line + " L600 58 L" + points[0][0] + " 58 Z");
}
function renderBalance(s) {
  const key = [s.genesis, s.address || "", s.unit].join(":");
  // A snapshot never carries across wallets, chains or a QDAY denomination change.
  if (balanceSnapshot?.key !== key) balanceSnapshot = null;
  if (s.balanceReady) balanceSnapshot = {key, balance:s.balance, immature:s.immature, pending:s.pending};
  for (const id of ["balance", "immature", "pending"]) $(id).textContent = balanceSnapshot ? coins(balanceSnapshot[id]) : "—";
  document.querySelectorAll("[data-balance]").forEach(el => {el.textContent = (balanceSnapshot ? coins(balanceSnapshot.balance) : "—") + " QDAY";});
  $("balanceNote").textContent = s.balanceReady ? "Available to send" : balanceSnapshot ? "Updating balance… Showing the last known amounts." : "Calculating your balance…";
}
function setWalletScreenLocked(locked) {
  document.body.classList.toggle("wallet-locked", locked);
  for (const root of document.querySelectorAll(".sidebar, .workspace")) {
    root.inert = locked;
    if (locked) root.setAttribute("aria-hidden", "true"); else root.removeAttribute("aria-hidden");
  }
}
function setUnlockMode(recovery = false) {
  $("unlock").querySelectorAll("input, textarea").forEach(el => {el.value = "";});
  lockedRecoveryAddress = recovery ? latest?.address || "" : "";
  $("unlockForm").hidden = recovery;
  $("lockedRecoveryForm").hidden = !recovery;
  $("recoverLocked").hidden = recovery;
  $("backToUnlock").hidden = !recovery;
  $("unlockTitle").textContent = recovery ? "Recover or import wallet." : "Unlock your wallet.";
  $("unlockDescription").textContent = recovery ? "Enter any QDAY 24-word seed phrase and set a new password. The wallet derived from those words becomes active." : "Enter your wallet password to access your wallet. Mining and DEFEND are stopped.";
}
function showLockedWallet() {
  setWalletScreenLocked(true);
  hideBackup(); transferDraft = null; proofDraft = null; burnDraft = null;
  // A lock from this tab or another tab also closes any open wallet dialog.
  document.querySelectorAll("dialog[open]").forEach(el => {if (el.id !== "unlock") el.close();});
  if (!$("unlock").open) {
    setUnlockMode();
    $("unlock").showModal();
    $("unlockPassword").focus();
  }
}
function render(s) {
  latest = s; connected = true;
  if (lastConnectionError && $("messageText").textContent === lastConnectionError) {
    $("messageText").textContent = "";
    $("message").hidden = true;
  }
  lastConnectionError = "";
  const locked = s.hasWallet && (!s.unlocked || lockRequested);
  setWalletScreenLocked(locked);
  $("connect").hidden = true; $("app").hidden = false;
  $("connectionDot").classList.remove("offline");
  $("connectionLabel").textContent = "Node connected";
  $("network").textContent = s.network.toUpperCase();
  $("receiveNetwork").textContent = s.network;
  const p2p = s.p2p;
  $("networkPeers").textContent = p2p ? `${p2p.regularPeers} ${p2p.regularPeers === 1 ? "peer" : "peers"} · ${p2p.bootstrapPeers} ${p2p.bootstrapPeers === 1 ? "seed" : "seeds"}` : "Connecting…";
  $("networkPort").textContent = p2p?.mapping?.port ?? p2p?.listenAddress?.split(":").pop() ?? "—";
  $("networkInbound").textContent = p2p?.inboundPeers ?? 0;
  const mappingLabels = {discovering:"Finding router",mapped:"UPnP active","upstream-nat":"UPnP active · upstream NAT",unavailable:"Unavailable",disabled:"Disabled","not-needed":"Not applied"};
  $("networkMapping").textContent = mappingLabels[p2p?.mapping?.state] ?? "Checking…";
  $("networkMappingNote").textContent = p2p?.mapping?.detail ?? "Your wallet discovers peers through seeds, then releases them once other connections are stable.";
  $("networkRelayNotice").hidden = !s.relayError;
  $("networkRelayNotice").textContent = s.relayError ? "Broadcast is waiting for a working connection. Pending transactions retry automatically." : "";
  $("networkRelayNotice").title = s.relayError || "";
  for (const id of ["mode","height","peers"]) {
    $(id).textContent = s[id];
  }
  renderFees(s);
  renderBalance(s);
  $("hashrate").textContent = Math.round(s.hashrate).toLocaleString();
  $("blocks").textContent = s.blocksFound;
  $("sync").textContent = s.synced ? "SYNCED" : "SYNCING";
  if (s.hasWallet) $("setup").close();
  if (locked) {
    showLockedWallet();
  } else {
    $("unlock").close();
    if (!s.hasWallet && !setupDismissed && !busy && !backupPending && !document.querySelector("dialog[open]")) $("setup").showModal();
  }
  $("address").textContent = s.address || "Create a wallet to receive QDAY.";
  $("threads").max = s.maxThreads;
  if (!chosenThreads && s.threads) $("threads").value = s.threads;
  $("challengePoint").textContent = s.canary;
  $("proofFee").textContent = coins(s.proofFee);
  $("proofState").textContent = s.qday ? "QDAY ACTIVATED" : s.qdayHeight ? "PROOF CONFIRMED" : s.proofPending ? "IN MEMPOOL" : "AWAITING PROOF";
  $("proofProgress").hidden = !s.qdayHeight && !s.proofPending;
  $("proofProgress").textContent = s.qdayHeight ? (s.qday ? "QDAY activated at block " : "Proof confirmed. QDAY activates at block ") + s.qdayHeight + "." : s.proofPending ? "In this node's mempool, awaiting inclusion in a block. Transaction: " + s.proofPending : "";
  $("proofForm").hidden = !!s.qdayHeight;
  for (const [id, value] of Object.entries({miningReward:coins(s.blockReward) + " QDAY + fees",miningInterval:s.blockIntervalSeconds + " seconds",miningDifficulty:coins(s.difficulty),miningAdjustment:s.difficultyAlgorithm,miningMaturity:s.maturityBlocks + " blocks"})) $(id).textContent = value;
  $("start").textContent = s.qday ? "DEFEND" : "MINE";
  $("mode").classList.toggle("running", s.mode !== "STOP");
  $("miningDot").hidden = s.mode === "STOP";
  $("multiplier").hidden = !s.qday;
  $("shield").textContent = !s.qday ? "BEFORE PQ DAY" : !s.balanceReady ? "UPDATING…" : !s.shieldUntil ? "NO COINS" : s.shieldUntil <= s.height ? "DECAYING" : (s.shieldUntil - s.height) + " BLOCKS";
  document.body.classList.toggle("qday", s.qday);
  $("era").textContent = s.qday ? "THE AFTERPARTY IS LIVE" : s.qdayHeight ? "QDAY AT BLOCK " + s.qdayHeight : "WAITING FOR THE END";
  $("headline").textContent = s.qday ? "THE KEYS DIED.\nTHE PARTY DIDN’T." : "MINE TODAY.\nWITNESS TOMORROW.";
  $("story").textContent = s.qday ? "Your reserve key still works.\nYour CPU is now life support." : "PQ day is inevitable.\nYou're celebrating it with us.";
  $("canaryHeadline").textContent = s.qday ? "The rules changed.\nStay alive." : s.qdayHeight ? "The proof is in.\nGet ready." : "The keys are\nstill breathing.";
  $("canaryNote").textContent = s.qday ? "A verified solution activated QDAY. Keep DEFEND running to renew your shields before coins decay." : "The network is waiting for a verified break of its classical cryptographic challenge.";
  $("survivalEra").textContent = s.qday ? "PQ DAY ACTIVATED AT BLOCK " + s.qdayHeight : s.qdayHeight ? "ACTIVATION AT BLOCK " + s.qdayHeight : "BEFORE PQ DAY";
  $("survivalTitle").textContent = s.qday ? "Your afterlife is your responsibility." : "Mine while the keys still work.";
  $("survivalNote").textContent = s.qday ? "The challenge has been solved. Your reserve key protects spending; DEFEND renews your shields. Only a confirmed renewal prevents decay." : "QDAY starts when the network verifies a solution to its fixed Edwards25519 challenge. No news feed, admin switch or calendar date.";
  $("activityNote").textContent = s.qday ? "DEFEND renews shields and mines blocks. Stay online and unlocked. Renewals pay a fee. STOP lets shields expire; burned coins never return." : "Mining uses your CPU and electricity. Rewards require finding a block and waiting for maturity.";
  controls(); drawHashrate(s.hashrate);
  if (!s.lastError) lastNodeError = "";
  else if (s.lastError !== lastNodeError && !busy && !document.querySelector("dialog[open]")) {
    lastNodeError = s.lastError;
    message(s.lastError, true);
  }
}
async function refresh() {
  if (!token || stopped || busy) return;
  if (polling) return polling;
  polling = (async () => {
    try { render(await api("status")); }
    catch (e) {
      connected = false; controls();
      $("connectionDot").classList.add("offline");
      $("connectionLabel").textContent = "Disconnected";
      $("sync").textContent = "OFFLINE";
      if (e.message !== lastConnectionError && !busy && !document.querySelector("dialog[open]")) {
        lastConnectionError = e.message;
        message(e.message, true);
      }
    } finally { polling = null; }
  })();
  return polling;
}
async function action(fn) {
  if (busy || stopped) return;
  busy = true; controls(); message("Working…", false, true);
  // Finish any earlier status request before changing wallet state.
  try { await polling; await fn(); } catch (e) { message(e.message, true); }
  finally { busy = false; await refresh(); controls(); }
}
function showBackup(phrase, mustSave = false) {
  $("setup").close(); $("unlock").close(); $("recoveryPrompt").close();
  backupMustSave = mustSave;
  $("backupTitle").textContent = mustSave ? "Save your seed phrase." : "Your seed phrase.";
  $("savedBackup").textContent = mustSave ? "I saved my seed phrase" : "Done";
  backupPending = true; $("phrase").replaceChildren();
  phrase.trim().split(/\s+/).forEach((word, i) => {
    const cell = document.createElement("span"), number = document.createElement("i"), value = document.createElement("b");
    number.textContent = String(i + 1).padStart(2, "0");
    value.textContent = word;
    cell.append(number, value); $("phrase").append(cell);
  });
  if (!$("backup").open) $("backup").showModal();
  controls(); message();
}
function hideBackup() {
  $("backup").close(); $("phrase").replaceChildren(); backupPending = false; backupMustSave = false;
}
function setupChoice(mode) {
  setupMode = mode;
  $("restoreFields").hidden = mode !== "restore";
  $("restorePhrase").required = mode === "restore";
  $("newWalletNote").hidden = mode !== "create";
  $("setupButtonText").textContent = mode === "restore" ? "Restore wallet" : "Create wallet";
  document.querySelectorAll("[data-setup]").forEach(el => {el.setAttribute("aria-pressed", String(el.dataset.setup === mode));});
  message();
}
document.querySelectorAll("[data-setup]").forEach(el => {el.onclick = () => setupChoice(el.dataset.setup);});
$("browseSetup").onclick = () => {setupDismissed = true;$("setup").close();};
for (const name of ["setup", "unlock"]) {
  $(name).addEventListener("cancel", e => {
    if (busy || name === "unlock") {e.preventDefault();return;}
    setupDismissed = true;
  });
  $(name).addEventListener("close", () => {
    if ($(name).open) return;
    $(name).querySelectorAll("input, textarea").forEach(el => {el.value = "";});
    if (name === "unlock") {
      lockedRecoveryAddress = "";
      if (latest?.hasWallet && !latest.unlocked && !stopped) showLockedWallet();
    }
  });
}
$("backup").addEventListener("cancel", e => {e.preventDefault();if (!backupMustSave) hideBackup();});
function visiblePhrase() {
  if (!$("backup").open) return "";
  const words = Array.from(document.querySelectorAll("#phrase b")).map(el => el.textContent);
  return words.length === 24 ? words.join(" ") : "";
}
$("copySeed").onclick = async () => {
  const phrase = visiblePhrase();
  if (!phrase) return;
  try {await navigator.clipboard.writeText(phrase); message("Seed phrase copied. Keep your clipboard private.");}
  catch {message("Clipboard access was blocked. Select the words or download the text file.", true);}
};
$("downloadSeed").onclick = () => {
  const phrase = visiblePhrase();
  if (!phrase) return;
  const url = URL.createObjectURL(new Blob([phrase + "\n"], {type:"text/plain;charset=utf-8"}));
  const link = document.createElement("a"); link.href = url; link.download = "qday-seed-phrase.txt";
  document.body.append(link); link.click(); link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
};
$("savedBackup").onclick = () => {hideBackup(); message(); refresh();};
$("openRecovery").onclick = () => {message(); $("recoveryPrompt").showModal();};
$("cancelRecovery").onclick = () => $("recoveryPrompt").close();
$("openImport").onclick = () => {
  importAddress = latest?.address || "";
  $("importWarning").hidden = !latest?.hasWallet;
  $("importSubmit").textContent = latest?.hasWallet ? "Import & replace wallet" : "Import wallet";
  message(); $("importWallet").showModal();
};
$("cancelImport").onclick = () => $("importWallet").close();
for (const name of ["recoveryPrompt", "importWallet"]) {
  $(name).addEventListener("cancel", e => {if (busy) e.preventDefault();});
  $(name).addEventListener("close", () => {
    if ($(name).open) return;
    $(name).querySelectorAll("input, textarea").forEach(el => {el.value = "";});
    if (name === "importWallet") importAddress = "";
  });
}
$("importForm").onsubmit = e => {
  e.preventDefault(); action(async () => {
    if ($("importPassword").value !== $("importConfirm").value) throw new Error("Passwords do not match. Enter the same password twice.");
    await api("restore", {password:$("importPassword").value, phrase:$("importPhrase").value.trim(), replaceAddress:importAddress});
    $("importWallet").close(); samples.length = 0; transferDraft = null; proofDraft = null; burnDraft = null;
    message("Wallet imported. Mining and DEFEND are stopped.");
  });
};
$("threads").oninput = () => {chosenThreads = true;};
$("connectForm").onsubmit = e => {
  e.preventDefault(); token = $("token").value.trim(); sessionStorage.setItem("qdayToken", token); $("token").value = ""; message(); refresh();
};
$("createForm").onsubmit = e => {
  e.preventDefault(); action(async () => {
    if ($("createPassword").value !== $("confirmPassword").value) throw new Error("Passwords do not match. Enter the same password twice.");
    const value = await api("create", {password:$("createPassword").value, phrase:setupMode === "restore" ? $("restorePhrase").value.trim() : ""});
    showBackup(value.phrase, setupMode === "create");
  });
};
$("recoveryForm").onsubmit = e => {
  e.preventDefault(); action(async () => {
    try {
      const value = await api("recovery", {password:$("recoveryPassword").value});
      showBackup(value.phrase);
    } finally {$("recoveryPassword").value = "";}
  });
};
$("unlockForm").onsubmit = e => {
  e.preventDefault(); action(async () => {
    await api("unlock", {password:$("unlockPassword").value});
    $("unlockPassword").value = ""; message("Wallet unlocked.");
  });
};
$("recoverLocked").onclick = () => {
  if (busy || !connected || !latest?.hasWallet || latest.unlocked) return;
  setUnlockMode(true); message(); $("lockedSeed").focus();
};
$("backToUnlock").onclick = () => {
  if (busy) return;
  setUnlockMode(); message(); $("unlockPassword").focus();
};
$("lockedRecoveryForm").onsubmit = e => {
  e.preventDefault(); action(async () => {
    if ($("lockedNewPassword").value !== $("lockedConfirmPassword").value) throw new Error("Passwords do not match. Enter the same password twice.");
    const value = await api("restore", {password:$("lockedNewPassword").value, phrase:$("lockedSeed").value.trim(), replaceAddress:lockedRecoveryAddress});
    $("lockedRecoveryForm").querySelectorAll("input, textarea").forEach(el => {el.value = "";});
    samples.length = 0; transferDraft = null; proofDraft = null; burnDraft = null;
    message(value.address === lockedRecoveryAddress ? "Wallet recovered. Your new password is ready. Mining and DEFEND are stopped." : "Wallet imported and unlocked. Mining and DEFEND are stopped.");
  });
};
$("start").onclick = () => action(async () => {
  if (!$("threads").reportValidity()) {message("Choose a valid number of CPU threads.", true);return;}
  await api("start", {threads:Number($("threads").value)});
  message(latest?.qday ? "DEFEND started. Keep this wallet online and unlocked." : "CPU mining started.");
});
$("stop").onclick = async () => {
  try {await api("stop", {});message("CPU activity stopped.");await refresh();}
  catch(e) {message(e.message, true);}
};
function openWalletAction() {
  if (!connected || busy || stopped) return;
  if (!latest?.hasWallet) {setupDismissed = false;$("setup").showModal();message();return;}
  if (!latest?.unlocked) {showLockedWallet();message();return;}
  $("lockPrompt").showModal(); message();
}
$("lock").onclick = openWalletAction;
$("miningWalletAction").onclick = openWalletAction;
$("cancelLock").onclick = () => $("lockPrompt").close();
$("lockPrompt").addEventListener("cancel", e => {if (busy) e.preventDefault();});
$("confirmLock").onclick = () => {
  if ($("confirmLock").disabled) return;
  lockRequested = true;
  showLockedWallet();
  $("unlockTitle").textContent = "Locking your wallet…";
  $("unlockDescription").textContent = "Stopping CPU activity and clearing unlocked keys.";
  action(async () => {
    try {
      await api("lock", {});
      render({...latest, unlocked:false, mode:"STOP", hashrate:0});
      setUnlockMode();
      message("Wallet locked. Enter your password to unlock.");
    } finally { lockRequested = false; }
  });
};
$("sendForm").onsubmit = e => {
  e.preventDefault();
  if ($("send").disabled) return;
  const address = $("destination").value.trim(), amount = $("amount").value.trim();
  if (!(/^(?:qday1[ac-hj-np-z02-9]{59}|QDAY1[AC-HJ-NP-Z02-9]{59})$/.test(address) || /^qday1[0-9a-fA-F]{136}$/.test(address))) {message("Enter a valid QDAY recipient address.", true); return;}
  if (!/^(0|[1-9]\d*)(\.\d+)?$/.test(amount) || !/[1-9]/.test(amount)) {message("Enter a positive decimal amount.", true);return;}
  let review;
  try { review = reviewPayment("send", amount); }
  catch (error) { message(error.message, true); return; }
  transferDraft = {address, amount, fee:review.fee, unit:latest.unit, fromAddress:latest.address};
  $("reviewAmount").textContent = coins(amount); $("reviewAddress").textContent = address;
  $("reviewFee").textContent = coins(review.fee) + " QDAY";
  $("reviewTotal").textContent = coins(review.total) + " QDAY";
  message(); $("sendReview").showModal();
};
$("cancelTransfer").onclick = () => $("sendReview").close();
$("sendReview").addEventListener("close", () => {if (!$("sendReview").open) transferDraft = null;});
$("confirmTransfer").onclick = () => action(async () => {
  if (!transferDraft) return;
  if (transferDraft.unit !== latest.unit) {
    $("sendReview").close(); throw new Error("QDAY changed the denomination. Review your amount again.");
  }
  const value = await api("send", transferDraft);
  $("sendReview").close(); $("amount").value = "";
  resetFee("send");
  message("Submitted; waiting for a block. Transaction: " + value.transaction);
});
$("burnForm").onsubmit = e => {
  e.preventDefault();
  if ($("reviewBurn").disabled) return;
  const amount = $("burnAmount").value.trim();
  if (!/^(0|[1-9]\d*)(\.\d+)?$/.test(amount) || !/[1-9]/.test(amount)) {message("Enter a positive decimal amount to burn.", true);return;}
  let review;
  try { review = reviewPayment("burn", amount); }
  catch (error) { message(error.message, true); return; }
  burnDraft = {amount, fee:review.fee, unit:latest.unit, fromAddress:latest.address};
  $("burnReviewAmount").textContent = coins(amount);
  $("burnReviewFee").textContent = coins(review.fee) + " QDAY";
  $("burnReviewTotal").textContent = coins(review.total) + " QDAY";
  $("burnConfirmation").value = "";
  controls(); message(); $("burnReview").showModal(); $("burnConfirmation").focus();
};
$("burnConfirmation").oninput = controls;
$("cancelBurn").onclick = () => $("burnReview").close();
$("burnReview").addEventListener("close", () => {
  if ($("burnReview").open) return;
  burnDraft = null; $("burnConfirmation").value = ""; controls();
});
$("confirmBurn").onclick = () => action(async () => {
  if (!burnDraft || $("burnConfirmation").value.trim() !== "BURN") return;
  if (burnDraft.unit !== latest.unit) {
    $("burnReview").close(); throw new Error("QDAY changed the denomination. Review the burn amount again.");
  }
  const value = await api("burn", burnDraft);
  $("burnReview").close(); $("burnAmount").value = "";
  resetFee("burn");
  message("Burn submitted; supply changes after confirmation. Transaction: " + value.transaction);
});
$("copyAddress").onclick = async () => {
  if (!latest?.address) return;
  try {await navigator.clipboard.writeText(latest.address);message("Address copied.");}
  catch {const range=document.createRange();range.selectNodeContents($("address"));getSelection().removeAllRanges();getSelection().addRange(range);message("Address selected. Press Ctrl+C to copy.");}
};
$("copyChallenge").onclick = async () => {
  try {await navigator.clipboard.writeText(latest.canary);message("Challenge point copied.");}
  catch {message("Select the challenge point and press Ctrl+C to copy.");}
};
$("proofForm").onsubmit = e => {
  e.preventDefault(); if ($("verifyProof").disabled) return;
  action(async () => {
    const witness = $("proofWitness").value.trim().toLowerCase();
    if (!/^[0-9a-f]{64}$/.test(witness)) throw new Error("Enter exactly 64 hexadecimal characters for the solution.");
    const result = await api("proof/verify", {witness});
    if (result.qdayHeight) throw new Error("A proof is already confirmed on this network.");
    if (result.proofPending) throw new Error("A proof is already awaiting confirmation in the mempool.");
    const fee = selectedFee("proof", result.fee, result.unit);
    proofDraft = {witness, fee, unit:result.unit, fromAddress:latest?.address || ""};
    $("proofReviewNetwork").textContent = result.network;
    $("proofReviewFee").textContent = coins(fee) + " QDAY";
    $("proofReviewDelay").textContent = result.activationDelay + (result.activationDelay === 1 ? " block" : " blocks");
    $("proofReviewWitness").textContent = witness;
    message(); $("proofReview").showModal();
  });
};
$("cancelProof").onclick = () => $("proofReview").close();
$("proofReview").addEventListener("close", () => {if (!$("proofReview").open) proofDraft = null;});
for (const name of ["sendReview", "burnReview", "proofReview"]) $(name).addEventListener("cancel", e => {if (busy) e.preventDefault();});
$("confirmProof").onclick = () => action(async () => {
  if (!proofDraft) return;
  const value = await api("proof", proofDraft);
  $("proofReview").close(); $("proofWitness").value = "";
  resetFee("proof");
  message("Proof submitted; waiting for a block. Transaction: " + value.transaction);
});
$("peerForm").onsubmit = e => {
  e.preventDefault(); action(async () => {
    $("peerResult").classList.remove("error");
    $("peerResult").textContent = "Saving peer and trying to connect…";
    try {
      const result = await api("peers", {peer:$("peerAddress").value.trim()});
      $("peerResult").classList.toggle("error", !result.connected);
      $("peerResult").textContent = result.connected ? "Connected to " + result.address + ". Address saved for future connections." : "Saved " + result.address + ". Connection failed: " + result.connectionError + ". The node will try again through peer discovery.";
      $("peerAddress").value = ""; message();
    } catch (e) {
      $("peerResult").classList.add("error"); $("peerResult").textContent = e.message;
      throw e;
    }
  });
};
$("restart").onclick = () => {$("restartPrompt").showModal();message();};
$("cancelRestart").onclick = () => $("restartPrompt").close();
$("restartPrompt").addEventListener("cancel", e => {if (busy) e.preventDefault();});
function showClosed(restarting = false) {
  stopped = true; connected = false; token = "";
  setWalletScreenLocked(false);
  sessionStorage.removeItem("qdayToken"); hideBackup(); $("sendReview").close(); $("burnReview").close();
  document.querySelectorAll("dialog[open]").forEach(el => el.close());
  $("app").hidden = true; $("connect").hidden = true; $("closed").hidden = false;
  $("closedTitle").textContent = restarting ? "Restarting QDAY…" : "Your outpost is offline.";
  $("closedNote").textContent = restarting ? "Your wallet will reopen automatically in the browser. Unlock it to resume mining or DEFEND. You can close this tab." : "QDAY has stopped. You can close this tab. Open QDAY Wallet to come back.";
  $("connectionDot").classList.add("offline"); $("connectionLabel").textContent = "Node stopped";
  message(); window.scrollTo({top:0,behavior:"instant"});
}
$("confirmRestart").onclick = () => action(async () => {
  await api("restart", {}); showClosed(true);
});
$("quit").onclick = $("quitLocked").onclick = () => action(async () => {
  await api("shutdown", {}); showClosed();
});
async function connect() {
  controls();
  if (launchCode) {
    try {
      const res = await fetch("/api/session", {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({code:launchCode}),cache:"no-store"});
      if (!res.ok) throw new Error("This launch link expired. Open QDAY Wallet again.");
      const data = await res.json(); token = data.token; sessionStorage.setItem("qdayToken", token);
    } catch(e) {message(e.message, true); return;}
  }
  await refresh();
}
connect(); setInterval(refresh, 2000);
