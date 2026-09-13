// Optional real Chromium UI check. No JS dependencies; Node 22's WebSocket
// speaks Chrome DevTools Protocol. All keys belong to disposable wallets with the exact mainnet rules.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const net = require('node:net');
const {spawn,spawnSync} = require('node:child_process');
const root = path.resolve(__dirname, '..');
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'qday-browser-'));
const processes = [];
let socket;
async function freePort() { const s=net.createServer();await new Promise(r=>s.listen(0,'127.0.0.1',r));const p=s.address().port;await new Promise(r=>s.close(r));return p; }
async function until(fn, label, seconds=20) { const end=Date.now()+seconds*1000;while(Date.now()<end){try{const value=await fn();if(value)return value;}catch{}await sleep(150);}throw Error('Timed out: '+label); }
async function main() {
  const chromePort=await freePort();
  const generated=spawnSync(path.join(root,'.tools/go/bin/go'),['run','./node/internal/testgenesis',temp],{cwd:root,stdio:'inherit'});
  if(generated.status!==0)throw Error('Test genesis generation failed');
  const manifest=path.join(temp,'test-mainnet.json'), seedData=path.join(temp,'seed'), seedPort=await freePort();
  const testManifest=JSON.parse(fs.readFileSync(manifest,'utf8'));
  if(testManifest.genesisMessage!=="PQ DAY IS INEVITABLE. YOU'RE CELEBRATING IT WITH ME." || testManifest.genesis.transactions[0].arbitraryData.length!==2)throw Error('Disposable genesis inscription is missing');
  const seedLog=fs.openSync(path.join(root,'build/browser-seed.log'),'w');
  const seedNode=spawn(path.join(root,'build/qday'),['--network',manifest,'--data',seedData,'--http','127.0.0.1:0','--p2p',`127.0.0.1:${seedPort}`,'--seed-node','--seeds','','--peers','','--upnp=false'],{stdio:['ignore',seedLog,seedLog]});processes.push(seedNode);
  const seedAPI=async(route,body)=>{
    const endpoint=JSON.parse(fs.readFileSync(path.join(seedData,'node.json'),'utf8'));
    const token=fs.readFileSync(path.join(seedData,'api.token'),'utf8').trim();
    const res=await fetch(endpoint.url+'/api/'+route,{method:body===undefined?'GET':'POST',headers:{Authorization:'Bearer '+token,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
    if(!res.ok)throw Error('Test funding node request failed: '+route);
    return res.json();
  };
  await until(()=>seedAPI('status'),'test seed startup');
  const otherPhrase=JSON.parse(fs.readFileSync(path.join(temp,'test-owner.json'),'utf8')).phrase;
  await seedAPI('create',{password:'temporary-funding-test-password',phrase:otherPhrase});
  const nodeLog=fs.openSync(path.join(root,'build/browser-node.log'),'w');
  const launchFile=path.join(temp,'browser-url'), browser=path.join(temp,'capture-browser');
  fs.writeFileSync(browser, '#!/usr/bin/env node\nrequire("node:fs").writeFileSync(process.env.QDAY_TEST_LAUNCH_URL,process.argv[2],{mode:0o600});\n',{mode:0o700});
  const node=spawn(path.join(root,'build/qday-wallet'),['--network',manifest,'--seeds',`127.0.0.1:${seedPort}`,'--upnp=false','--node',path.join(root,'build/qday'),'--data',path.join(temp,'data'),'--browser',browser],{env:{...process.env,QDAY_TEST_LAUNCH_URL:launchFile},stdio:['ignore',nodeLog,nodeLog]});processes.push(node);
  const tokenFile=path.join(temp,'data/api.token');await until(()=>fs.existsSync(tokenFile),'node startup');
  const token=fs.readFileSync(tokenFile,'utf8').trim();
  const launchURL=await until(()=>fs.existsSync(launchFile)&&fs.readFileSync(launchFile,'utf8'),'automatic browser launch');
  if(launchURL.includes(token))throw Error('Persistent credential in browser URL');
  const chrome=spawn(process.env.CHROME||'/usr/bin/google-chrome',['--headless=new','--no-sandbox','--disable-gpu','--no-proxy-server',`--remote-debugging-port=${chromePort}`,`--user-data-dir=${path.join(temp,'chrome')}`,'about:blank'],{stdio:'ignore'});processes.push(chrome);
  const target=await until(async()=>{const list=await(await fetch(`http://127.0.0.1:${chromePort}/json/list`)).json();return list.find(t=>t.type==='page');},'Chromium startup');
  socket=new WebSocket(target.webSocketDebuggerUrl);await new Promise((resolve,reject)=>{socket.onopen=resolve;socket.onerror=reject;});
  let seq=0;const pending=new Map();const errors=[];
  socket.onmessage=event=>{const m=JSON.parse(event.data);if(m.id){const p=pending.get(m.id);if(p){pending.delete(m.id);m.error?p.reject(Error(JSON.stringify(m.error))):p.resolve(m.result);}}else if(m.method==='Runtime.exceptionThrown'){errors.push(m.params.exceptionDetails.text);}};
  const cdp=(method,params={})=>new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});socket.send(JSON.stringify({id,method,params}));});
  const evaluate=async expression=>{const r=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true,userGesture:true});if(r.exceptionDetails)throw Error(r.exceptionDetails.text);return r.result.value;};
  await cdp('Runtime.enable');await cdp('Page.enable');await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:1120,deviceScaleFactor:1,mobile:false});
  const downloads=path.join(temp,'downloads');fs.mkdirSync(downloads);
  await cdp('Browser.setDownloadBehavior',{behavior:'allow',downloadPath:downloads});
  const screenshot=async(file,width=1440)=>{const layout=await cdp('Page.getLayoutMetrics');const shot=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:true,clip:{x:0,y:0,width,height:layout.cssContentSize.height,scale:1}});fs.writeFileSync(path.join(root,'build',file),Buffer.from(shot.data,'base64'));};
  await cdp('Page.navigate',{url:launchURL});
  await until(()=>evaluate('!!document.getElementById("connectForm")'),'UI load');
  await until(()=>evaluate('!document.getElementById("app").hidden'),'UI authenticated');
  if(await evaluate('location.hash')!=='')throw Error('Launch code remained in URL');
  if(await evaluate('sessionStorage.getItem("qdayToken")')===token)throw Error('Browser session uses root credential');
  await evaluate('document.fonts.ready');
  if(await evaluate('getComputedStyle(document.documentElement).fontSize')!=="15px")throw Error('Base font size did not increase by 2px');
  await until(()=>evaluate('Array.from(document.images).every(i=>i.complete&&i.naturalWidth>0)'),'embedded logo load');
  if(!await evaluate('fetch("/assets/hero.png").then(r=>r.ok)'))throw Error('Embedded artwork missing');
  await until(()=>evaluate('document.getElementById("setup").open'),'onboarding modal');
  if(!await evaluate('document.getElementById("setup").matches(":modal") && getComputedStyle(document.getElementById("setup"),"::backdrop").backgroundColor !== "rgba(0, 0, 0, 0)"'))throw Error('Onboarding did not dim the interface');
  const overviewTop=await evaluate('document.getElementById("view-overview").getBoundingClientRect().top');
  await screenshot('wallet-onboarding.png');
  await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true});
  if(await evaluate('document.documentElement.scrollWidth>innerWidth || document.getElementById("setup").scrollWidth>document.getElementById("setup").clientWidth'))throw Error('Mobile onboarding overflow');
  await screenshot('wallet-onboarding-mobile.png',390);
  await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:1120,deviceScaleFactor:1,mobile:false});
  await evaluate(`document.querySelector('[data-setup="restore"]').click();document.getElementById('restorePhrase').value='abandon '.repeat(24).trim();document.getElementById('createPassword').value='temporary-browser-passphrase';document.getElementById('confirmPassword').value='temporary-browser-passphrase';document.getElementById('createForm').requestSubmit()`);
  await until(()=>evaluate('document.querySelector("#setup .dialog-message.error")?.textContent.length > 0 && !busy'),'invalid recovery checksum in modal');
  if(!await evaluate('document.getElementById("setup").open && !latest.hasWallet'))throw Error('Invalid recovery created a wallet');
  await evaluate(`document.querySelector('[data-setup="create"]').click();document.getElementById('confirmPassword').value='mismatched-test-password';document.getElementById('createForm').requestSubmit()`);
  await until(()=>evaluate('document.querySelector("#setup .dialog-message").textContent.startsWith("Passwords do not match") && !busy'),'password confirmation');
  await evaluate(`document.getElementById('confirmPassword').value='temporary-browser-passphrase';document.getElementById('createForm').requestSubmit()`);
  await until(()=>evaluate('document.getElementById("backup").open'),'wallet creation');
  if(await evaluate('document.querySelectorAll("#phrase b").length')!==24)throw Error('Recovery words missing');
  const recoveryWords=await evaluate('Array.from(document.querySelectorAll("#phrase b")).map(el=>el.textContent).join(" ")');
  if(await evaluate('document.getElementById("view-overview").getBoundingClientRect().top')!==overviewTop)throw Error('Onboarding shifted the dashboard');
  await evaluate('document.getElementById("downloadSeed").click()');
  await until(()=>fs.existsSync(path.join(downloads,'qday-seed-phrase.txt')),'explicit seed backup download');
  if(fs.readFileSync(path.join(downloads,'qday-seed-phrase.txt'),'utf8').trim()!==recoveryWords)throw Error('Downloaded seed does not match the wallet');
  await evaluate(`document.getElementById('savedBackup').click()`);
  await until(()=>evaluate('!document.getElementById("start").disabled'),'backup acknowledged');
  if(await evaluate('document.getElementById("setup").open || document.getElementById("phrase").textContent || document.getElementById("createPassword").value || document.getElementById("restorePhrase").value'))throw Error('Setup or secret words remained visible');
  const launchGate=await evaluate(`(() => {
    const current=latest;
    latest={...current,genesisReady:false,genesisWaitSeconds:901,genesisTimestamp:new Date(Date.now()+901000).toISOString()};
    controls();
    const result=document.getElementById('start').disabled && document.getElementById('miningNoticeText').textContent.startsWith('Mining opens in 15m 1s');
    latest=current;controls();
    return result;
  })()`);
  if(!launchGate)throw Error('Pre-genesis mining gate was not visible in the wallet');
  const walletAddress=await evaluate('latest.address');
  await evaluate(`document.getElementById('threads').value='1';document.getElementById('start').click()`);
  await until(()=>evaluate('Number(document.getElementById("height").textContent)>=4'),'browser CPU mining');
  await seedAPI('send',{address:walletAddress,amount:'100',unit:(await seedAPI('status')).unit});
  await until(()=>evaluate('Number(latest.balance)>=100'),'confirmed fixture funding',60);
  const balanceChecks = await evaluate(`(() => {
    const good = latest, before = document.getElementById('balance').textContent;
    const updating = {...good, balanceReady:false, balance:null, immature:null, pending:null};
    renderBalance(updating);
    const retained = document.getElementById('balance').textContent === before && document.getElementById('balanceNote').textContent.includes('Updating');
    renderBalance({...good, balanceReady:true, balance:'0', immature:'0', pending:'0'});
    const realZero = document.getElementById('balance').textContent === '0';
    renderBalance(good);
    renderBalance({...updating, address:'different-test-address'});
    const walletReset = document.getElementById('balance').textContent === '—';
    renderBalance(good);
    renderBalance({...updating, unit:'different-test-denomination'});
    const unitReset = document.getElementById('balance').textContent === '—';
    renderBalance(good);
    return retained && realZero && walletReset && unitReset;
  })()`);
  if(!balanceChecks)throw Error('Balance updates flashed zero, hid a real zero or reused a different wallet/denomination');
  await evaluate('message()');
  await screenshot('wallet-before.png');
  await evaluate('document.querySelector("nav [data-view=receive]").click()');
  if(!await evaluate('!document.getElementById("view-receive").hidden && document.getElementById("address").textContent.startsWith("qday1p") && document.getElementById("address").textContent.length === 64'))throw Error('Receive view failed');
  await evaluate('document.querySelector("nav [data-view=send]").click();document.getElementById("destination").value=document.getElementById("address").textContent;document.getElementById("amount").value="1";document.getElementById("sendForm").requestSubmit()');
  await until(()=>evaluate('document.getElementById("sendReview").open'),'transfer review');
  if(!await evaluate('transferDraft.fee===latest.fee && document.getElementById("reviewTotal").textContent==="1.001 QDAY"'))throw Error('Automatic fee or transfer total is incorrect');
  await evaluate('document.getElementById("cancelTransfer").click()');
  await until(()=>evaluate('!document.getElementById("sendReview").open'),'cancel review');
  await evaluate(`document.querySelector('[data-fee="send"] [data-fee-mode="custom"]').click();document.querySelector('[data-fee="send"] [data-fee-input]').value='2.75'`);
  await evaluate('document.getElementById("sendForm").requestSubmit()');
  await until(()=>evaluate('document.getElementById("sendReview").open'),'transfer second review');
  if(!await evaluate('transferDraft.fee==="2.75" && document.getElementById("reviewFee").textContent==="2.75 QDAY" && document.getElementById("reviewTotal").textContent==="3.75 QDAY"'))throw Error('Custom fee changed between entry and review');
  await screenshot('wallet-custom-fee-review.png');
  await until(()=>evaluate('!document.getElementById("confirmTransfer").disabled'),'transfer ready');
  await evaluate('document.getElementById("confirmTransfer").click()');
  try {await until(()=>evaluate('document.getElementById("message").textContent.startsWith("Submitted;")'),'signed browser transfer');}
  catch(e){throw Error(e.message+': '+await evaluate('JSON.stringify({message:document.getElementById("message").textContent,busy,draft:!!transferDraft,disabled:document.getElementById("confirmTransfer").disabled})'));}
  await until(()=>evaluate('latest.balanceReady && latest.pending==="0"'),'transfer mined');
  if(!await evaluate(`document.querySelector('[data-fee="send"]').dataset.mode==='auto'`))throw Error('Completed transfer retained a custom fee');
  await evaluate('document.getElementById("stop").click()');
  await until(()=>evaluate('latest.mode==="STOP"'),'pause before proof review');
  await evaluate(`view('settings');document.getElementById('burnAmount').value='1';document.querySelector('[data-fee="burn"] [data-fee-mode="custom"]').click();document.querySelector('[data-fee="burn"] [data-fee-input]').value='-1';document.getElementById('burnForm').requestSubmit()`);
  if(!await evaluate(`!document.getElementById('burnReview').open && document.getElementById('messageText').textContent.includes('Fee must be')`))throw Error('Negative custom fee reached burn confirmation');
  await evaluate(`document.querySelector('[data-fee="burn"] [data-fee-input]').value='0.25';document.getElementById('burnForm').requestSubmit()`);
  await until(()=>evaluate('document.getElementById("burnReview").open'),'custom burn fee review');
  if(!await evaluate(`burnDraft.fee==='0.25' && document.getElementById('burnReviewTotal').textContent==='1.25 QDAY'`))throw Error('Burn review lost the custom fee');
  await evaluate('document.getElementById("cancelBurn").click()');
  const feeBoundaries=await evaluate(`(() => {
    const proof=document.querySelector('[data-fee="proof"]');setFeeMode(proof,'custom');proof.querySelector('[data-fee-input]').value='0.5';
    let minimum=false;try{selectedFee('proof',latest.proofFee);}catch(e){minimum=e.message.includes('at least');}
    renderFees({...latest,unit:'1000000000000000000'});
    const reset=[...document.querySelectorAll('[data-fee]')].every(f=>f.dataset.mode==='auto'&&!f.querySelector('[data-fee-input]').value);
    renderFees(latest);return minimum&&reset;
  })()`);
  if(!feeBoundaries)throw Error('Proof fee minimum or denomination reset failed');
  await evaluate(`view('send');document.querySelector('[data-fee="send"] [data-fee-mode="custom"]').click();document.querySelector('[data-fee="send"] [data-fee-input]').value='0.25'`);
  await screenshot('wallet-custom-fee-desktop.png');
  for(const width of [320,390]) {
    await cdp('Emulation.setDeviceMetricsOverride',{width,height:844,deviceScaleFactor:1,mobile:true});
    if(await evaluate('document.documentElement.scrollWidth>innerWidth'))throw Error('Custom fee form overflows at '+width);
  }
  await screenshot('wallet-custom-fee-mobile.png',390);
  await evaluate(`resetFee('send')`);
  await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:1120,deviceScaleFactor:1,mobile:false});
  for(const scalar of ['01','02']) {
    await evaluate(`view("survival");document.getElementById("proofWitness").value="${scalar}"+"00".repeat(31);document.getElementById("proofForm").requestSubmit()`);
    await until(()=>evaluate('document.getElementById("message").textContent.includes("does not solve") && !busy'),'invalid proof rejected');
    if(!await evaluate('!latest.proofPending && !latest.qdayHeight'))throw Error('Invalid proof entered the mempool');
  }
  await evaluate('view("settings");message()');
  await screenshot('wallet-network-settings.png');
  if(!await evaluate('document.querySelector(".topbar #quit") && getComputedStyle(document.querySelector(".topbar")).position === "sticky"'))throw Error('EXIT QDAY is not in the persistent top bar');
  await evaluate('document.getElementById("openImport").click()');
  if(!await evaluate('document.getElementById("importWallet").matches(":modal") && !document.getElementById("importWarning").hidden'))throw Error('Import warning or modal missing');
  await screenshot('wallet-import.png');
  await evaluate('document.getElementById("importPhrase").value="discarded test text";document.getElementById("cancelImport").click()');
  await until(()=>evaluate('!document.getElementById("importPhrase").value'),'cancel clears import phrase');

  if(!await evaluate('document.getElementById("networkPeers").textContent.includes("1 seed") && document.getElementById("networkMapping").textContent==="Disabled"'))throw Error('Network diagnostics are incorrect');
  await evaluate('document.getElementById("peerAddress").value="127.0.0.1:0";document.getElementById("peerForm").requestSubmit()');
  await until(()=>evaluate('!busy && document.getElementById("peerResult").classList.contains("error")'),'invalid manual peer');
  await evaluate(`document.getElementById('peerAddress').value='127.0.0.1:${seedPort}';document.getElementById('peerForm').requestSubmit()`);
  await until(()=>evaluate('!busy && document.getElementById("peerResult").textContent.startsWith("Connected to")'),'manual peer connection');
  await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true});
  for(const name of ['overview','send','receive','survival','settings']) {
    await evaluate(`document.querySelector('nav [data-view="${name}"]').click()`);
    if(await evaluate('document.documentElement.scrollWidth > innerWidth'))throw Error('Mobile horizontal overflow: '+name);
    await evaluate('window.scrollTo(0, document.body.scrollHeight)');
    if(!await evaluate('document.getElementById("quit").getBoundingClientRect().top >= 0 && document.getElementById("quit").getBoundingClientRect().right <= innerWidth'))throw Error('EXIT QDAY disappeared while scrolling: '+name);

  }
  await evaluate('document.querySelector("nav [data-view=overview]").click()');
  const layout=await cdp('Page.getLayoutMetrics');
  const png=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:true,clip:{x:0,y:0,width:390,height:layout.cssContentSize.height,scale:1}});
  fs.writeFileSync(path.join(root,'build/wallet-mobile.png'),Buffer.from(png.data,'base64'));
  await evaluate(`document.getElementById('stop').click()`);
  await until(()=>evaluate('document.getElementById("mode").textContent==="STOP"'),'browser STOP');
  if(!await evaluate('document.getElementById("lockIcon").getAttribute("href")==="#i-unlock" && document.getElementById("walletStateIcon").getAttribute("href")==="#i-unlock" && document.getElementById("lockText").textContent==="Unlocked"'))throw Error('Unlocked wallet shows a closed lock');
  await evaluate(`document.getElementById('lock').click()`);
  await until(()=>evaluate('document.getElementById("lockPrompt").open && latest.unlocked'),'lock confirmation');
  await screenshot('wallet-lock-confirmation.png',390);
  await evaluate('document.getElementById("cancelLock").click()');
  if(!await evaluate('latest.unlocked && !document.getElementById("lockPrompt").open'))throw Error('Cancel locked the wallet');
  await evaluate('document.getElementById("lock").click();document.getElementById("confirmLock").click()');
  if(!await evaluate('document.body.classList.contains("wallet-locked") && document.getElementById("unlock").open && document.querySelector(".workspace").inert'))throw Error('Lock click left wallet content visible while awaiting the node');
  await until(()=>evaluate('!busy && !latest.unlocked && document.getElementById("lockText").textContent==="Locked"'),'browser wallet lock');
  const lockScreen = 'document.getElementById("unlock").matches(":modal") && document.body.classList.contains("wallet-locked") && document.querySelector(".workspace").inert && document.querySelector(".sidebar").inert && getComputedStyle(document.getElementById("app")).visibility==="hidden" && document.querySelectorAll("dialog[open]").length===1';
  if(!await evaluate(lockScreen))throw Error('Lock did not immediately hide and block the wallet');
  await cdp('Input.dispatchKeyEvent',{type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
  await cdp('Input.dispatchKeyEvent',{type:'keyUp',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
  await cdp('Input.dispatchMouseEvent',{type:'mousePressed',x:8,y:8,button:'left',clickCount:1});
  await cdp('Input.dispatchMouseEvent',{type:'mouseReleased',x:8,y:8,button:'left',clickCount:1});
  if(!await evaluate(lockScreen))throw Error('Escape or clicking the backdrop dismissed the lock screen');
  await evaluate('document.querySelector("nav [data-view=settings]").click()');
  if(!await evaluate('document.getElementById("pageName").textContent==="Overview"'))throw Error('Locked wallet allowed navigation');
  for(let i=0;i<4;i++) {
    await cdp('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
    await cdp('Input.dispatchKeyEvent',{type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
    if(!await evaluate('document.activeElement===document.body || document.getElementById("unlock").contains(document.activeElement)'))throw Error('Keyboard focus escaped the lock screen');
  }
  await cdp('Page.reload');
  await until(()=>evaluate('latest?.hasWallet && !latest.unlocked && document.getElementById("unlock")?.open'),'lock screen after page reload');
  if(!await evaluate(lockScreen))throw Error('Reload exposed a locked wallet');
  const lockedShot=await cdp('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});
  fs.writeFileSync(path.join(root,'build/wallet-locked-mobile.png'),Buffer.from(lockedShot.data,'base64'));
  await evaluate('document.getElementById("unlockPassword").value="incorrect-test-password";document.getElementById("unlockForm").requestSubmit()');
  await until(()=>evaluate('!busy && !!document.querySelector("#unlock .dialog-message.error")?.textContent'),'wrong unlock password');
  if(!await evaluate(lockScreen))throw Error('Wrong password exposed wallet content');
  const errorIsolation = await evaluate(`(() => {
    const state=latest, local=document.querySelector('#unlockForm .dialog-message');
    const error=local.textContent;
    render({...state,lastError:'background diagnostic test'});
    const kept=local.textContent===error;
    render(state);
    return kept;
  })()`);
  if(!errorIsolation)throw Error('Background node diagnostic overwrote the password error');
  await evaluate('document.getElementById("unlockPassword").value="temporary-browser-passphrase";document.getElementById("unlockForm").requestSubmit()');
  await until(()=>evaluate('!busy && latest.unlocked && !document.getElementById("unlock").open && document.getElementById("lockText").textContent==="Unlocked" && !document.getElementById("start").disabled && !document.querySelector(".workspace").inert && getComputedStyle(document.getElementById("app")).visibility!=="hidden"'),'browser unlock');
  const noticeIsolation=await evaluate(`(() => {
    const state=latest;
    render({...state,lastError:'one-time diagnostic test'});
    const shown=document.getElementById('messageText').textContent==='one-time diagnostic test';
    document.getElementById('dismissMessage').click();
    for(let i=0;i<3;i++)render({...state,lastError:'one-time diagnostic test'});
    const dismissed=document.getElementById('message').hidden;
    render({...state,lastError:'different diagnostic test'});
    const next=document.getElementById('messageText').textContent==='different diagnostic test';
    render(state);message();
    return shown&&dismissed&&next;
  })()`);
  if(!noticeIsolation)throw Error('Dismissed diagnostics repeated or hid a different error');
  const reconnect=await evaluate(`(async () => {
    await polling;
    const originalFetch=window.fetch;
    window.fetch=async()=>{throw new Error('temporary connection test');};
    try {
      await refresh();
      const offline=!connected&&document.getElementById('sync').textContent==='OFFLINE';
      document.getElementById('dismissMessage').click();
      await refresh();
      return offline&&document.getElementById('message').hidden;
    } finally {window.fetch=originalFetch;await refresh();}
  })()`);
  if(!reconnect||!await evaluate('connected && !document.getElementById("start").disabled'))throw Error('Connection failure repeated notifications or left controls stuck after recovery');
  const boundedRequest=await evaluate(`(async () => {
    await polling;
    const originalFetch=window.fetch,originalTimer=window.setTimeout;
    window.fetch=(_url,options)=>new Promise((_resolve,reject)=>options.signal.addEventListener('abort',()=>reject(new DOMException('aborted','AbortError'))));
    window.setTimeout=(fn,ms,...args)=>originalTimer(fn,ms===15000?30:ms,...args);
    try {await api('status');return false;}
    catch(error){return error.message==='Local node is not responding. Reconnecting…';}
    finally{window.fetch=originalFetch;window.setTimeout=originalTimer;}
  })()`);
  if(!boundedRequest)throw Error('Unresponsive status request did not time out');
  for(const width of [320,390,768,1440]) {
    await cdp('Emulation.setDeviceMetricsOverride',{width,height:844,deviceScaleFactor:1,mobile:width<650});
    if(await evaluate('document.documentElement.scrollWidth>innerWidth || document.getElementById("lock").getBoundingClientRect().left<0 || document.getElementById("quit").getBoundingClientRect().right>innerWidth'))throw Error('Lock/EXIT top bar overflow at '+width);
  }
  await evaluate('view("overview");message()');
  await screenshot('wallet-unlocked.png');
  const oldPID=JSON.parse(fs.readFileSync(path.join(temp,'data/node.json'),'utf8')).pid;
  fs.unlinkSync(launchFile);
  await evaluate('view("settings");document.getElementById("restart").click()');
  await until(()=>evaluate('document.getElementById("restartPrompt").open'),'restart confirmation');
  await evaluate('document.getElementById("cancelRestart").click()');
  if(!await evaluate('latest.unlocked && !document.getElementById("restartPrompt").open'))throw Error('Cancel restarted the node');
  await evaluate('document.getElementById("restart").click();document.getElementById("confirmRestart").click()');
  await until(()=>evaluate('!document.getElementById("closed").hidden && document.getElementById("closedTitle").textContent.includes("Restarting")'),'restart handoff screen');
  const restartURL=await until(()=>fs.existsSync(launchFile)&&fs.readFileSync(launchFile,'utf8'),'automatic wallet reopen after restart',40);
  if(JSON.parse(fs.readFileSync(path.join(temp,'data/node.json'),'utf8')).pid===oldPID)throw Error('Restart did not replace native process');
  await cdp('Page.navigate',{url:restartURL});
  await until(()=>evaluate('document.getElementById("unlock")?.open && !latest.unlocked && latest.mode==="STOP"'),'locked wallet after restart');
  if(!await evaluate(lockScreen))throw Error('Restart exposed a locked wallet');
  if(await evaluate('latest.address')!==walletAddress)throw Error('Restart changed wallet address');
  await evaluate('document.getElementById("unlockPassword").value="temporary-browser-passphrase";document.getElementById("unlockForm").requestSubmit()');
  await until(()=>evaluate('!busy && latest.unlocked && latest.synced'),'unlock after restart');
  await evaluate('document.querySelector("nav [data-view=settings]").click();document.getElementById("quit").click()');
  await until(()=>evaluate('!document.getElementById("closed").hidden'),'browser EXIT QDAY');
  await until(()=>node.exitCode===0,'launcher graceful exit');
  // Restore the downloaded phrase through the UI of a separate empty node.
  fs.unlinkSync(launchFile);
  const restored=spawn(path.join(root,'build/qday-wallet'),['--network',manifest,'--seeds',`127.0.0.1:${seedPort}`,'--upnp=false','--node',path.join(root,'build/qday'),'--data',path.join(temp,'restored'),'--browser',browser],{env:{...process.env,QDAY_TEST_LAUNCH_URL:launchFile},stdio:['ignore',nodeLog,nodeLog]});processes.push(restored);
  const restoreURL=await until(()=>fs.existsSync(launchFile)&&fs.readFileSync(launchFile,'utf8'),'fresh restore node');
  await cdp('Page.navigate',{url:restoreURL});
  await until(()=>evaluate('document.getElementById("setup")?.open'),'restore onboarding');
  await evaluate(`document.querySelector('[data-setup="restore"]').click();document.getElementById('restorePhrase').value=${JSON.stringify(recoveryWords)};document.getElementById('createPassword').value='different-restored-password';document.getElementById('confirmPassword').value='different-restored-password';document.getElementById('createForm').requestSubmit()`);
  await until(()=>evaluate('document.getElementById("backup").open && !!latest.address'),'restored wallet backup');
  if(await evaluate('latest.address')!==walletAddress)throw Error('Recovery changed the wallet address');
  await evaluate('document.getElementById("savedBackup").click()');
  await until(()=>evaluate('!busy'),'restore complete');
  await evaluate('view("settings");document.getElementById("openRecovery").click();document.getElementById("recoveryPassword").value="incorrect-password";document.getElementById("recoveryForm").requestSubmit()');
  await until(()=>evaluate('document.getElementById("message").classList.contains("error") && !busy'),'backup password authentication');
  if(await evaluate('document.getElementById("backup").open || document.getElementById("recoveryPassword").value'))throw Error('Unauthorized recovery or password retained');
  await evaluate('document.getElementById("recoveryPassword").value="different-restored-password";document.getElementById("recoveryForm").requestSubmit()');
  await until(()=>evaluate('document.getElementById("backup").open && !busy'),'authenticated seed backup');
  if(await evaluate('Array.from(document.querySelectorAll("#phrase b")).map(el=>el.textContent).join(" ")')!==recoveryWords)throw Error('Restored recovery words changed');
  await cdp('Browser.grantPermissions',{origin:new URL(restoreURL).origin,permissions:['clipboardReadWrite','clipboardSanitizedWrite']});
  await cdp('Page.bringToFront');
  await evaluate('document.getElementById("copySeed").onclick()');
  if(await evaluate('navigator.clipboard.readText()')!==recoveryWords)throw Error('Seed phrase copy failed');
  // A lock requested by another browser tab must also close and clear recovery.
  await evaluate('api("lock", {})');
  await until(()=>evaluate('document.getElementById("unlock").open && !document.getElementById("backup").open && !document.getElementById("phrase").textContent'),'external lock clears recovery');
  if(!await evaluate(lockScreen))throw Error('External lock left wallet content accessible');
  const lockedKey=fs.readFileSync(path.join(temp,'restored/wallet.key'));
  await evaluate('document.getElementById("recoverLocked").click()');
  await until(()=>evaluate('!document.getElementById("lockedRecoveryForm").hidden && document.getElementById("unlockForm").hidden'),'locked seed import mode');
  if(!await evaluate('document.getElementById("unlockTitle").textContent==="Recover or import wallet." && document.getElementById("lockedRecoveryForm").textContent.includes("QDAY keeps no backup") && '+lockScreen))throw Error('Locked seed import warning or mandatory screen missing');
  for(const width of [320,390]) {
    await cdp('Emulation.setDeviceMetricsOverride',{width,height:844,deviceScaleFactor:1,mobile:true});
    if(await evaluate('document.documentElement.scrollWidth>innerWidth || document.getElementById("unlock").scrollWidth>document.getElementById("unlock").clientWidth'))throw Error('Locked seed import overflow at '+width);
  }
  const lockedImportScrolls=await evaluate(`(() => {const d=document.getElementById('unlock'),b=document.getElementById('backToUnlock');d.scrollTop=d.scrollHeight;const dr=d.getBoundingClientRect(),br=b.getBoundingClientRect();const visible=br.top>=dr.top&&br.bottom<=dr.bottom;d.scrollTop=0;return getComputedStyle(d).overflowY==='auto'&&d.scrollHeight>d.clientHeight&&visible;})()`);
  if(!lockedImportScrolls)throw Error('Locked seed import actions are not reachable by scrolling');
  await screenshot('wallet-locked-seed-import.png',390);
  await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:1120,deviceScaleFactor:1,mobile:false});
  await evaluate('document.getElementById("backToUnlock").click()');
  if(!await evaluate('!document.getElementById("unlockForm").hidden && document.getElementById("lockedRecoveryForm").hidden && !document.getElementById("lockedSeed").value && '+lockScreen))throw Error('Back to password did not clear locked import fields');
  await evaluate('document.getElementById("recoverLocked").click();document.getElementById("lockedSeed").value="abandon ".repeat(24).trim();document.getElementById("lockedNewPassword").value="locked-import-password";document.getElementById("lockedConfirmPassword").value="locked-import-password";document.getElementById("lockedRecoveryForm").requestSubmit()');
  await until(()=>evaluate('!busy && !!document.querySelector("#lockedRecoveryForm .dialog-message.error")?.textContent'),'invalid locked seed import');
  if(!fs.readFileSync(path.join(temp,'restored/wallet.key')).equals(lockedKey) || !await evaluate(lockScreen))throw Error('Invalid locked import changed or exposed the wallet');
  await evaluate(`document.getElementById('lockedSeed').value=${JSON.stringify(otherPhrase)};document.getElementById('lockedRecoveryForm').requestSubmit()`);
  await until(()=>evaluate(`!busy && latest.unlocked && latest.address!==${JSON.stringify(walletAddress)} && !document.getElementById("unlock").open`),'different wallet import from lock screen');
  if(!await evaluate('latest.mode==="STOP" && !document.getElementById("lockedSeed").value && !document.getElementById("lockedNewPassword").value'))throw Error('Locked import retained activity or secret fields');
  if(fs.readdirSync(path.join(temp,'restored')).some(n=>n.includes('backup')||n.startsWith('.qday-')))throw Error('Locked import retained an automatic backup');
  await evaluate('document.getElementById("lock").click();document.getElementById("confirmLock").click()');
  await until(()=>evaluate('!busy && document.getElementById("unlock").open'),'lock imported wallet');
  await evaluate(`document.getElementById('recoverLocked').click();document.getElementById('lockedSeed').value=${JSON.stringify(recoveryWords)};document.getElementById('lockedNewPassword').value='locked-return-password';document.getElementById('lockedConfirmPassword').value='locked-return-password';document.getElementById('lockedRecoveryForm').requestSubmit()`);
  await until(()=>evaluate(`!busy && latest.unlocked && latest.address===${JSON.stringify(walletAddress)} && !document.getElementById("unlock").open`),'saved seed returns from locked import');
  await evaluate('document.getElementById("openImport").click()');
  const oldKey=fs.readFileSync(path.join(temp,'restored/wallet.key'));
  await evaluate('document.getElementById("importPhrase").value="abandon ".repeat(24).trim();document.getElementById("importPassword").value="replacement-test-password";document.getElementById("importConfirm").value="replacement-test-password";document.getElementById("importForm").requestSubmit()');
  await until(()=>evaluate('document.querySelector("#importWallet .dialog-message.error")?.textContent.length > 0 && !busy'),'invalid settings import');
  if(!fs.readFileSync(path.join(temp,'restored/wallet.key')).equals(oldKey))throw Error('Invalid import replaced the wallet');
  await evaluate(`document.getElementById('importPhrase').value=${JSON.stringify(otherPhrase)};document.getElementById('importForm').requestSubmit()`);
  await until(()=>evaluate('!document.getElementById("importWallet").open && !busy'),'settings wallet replacement');
  if(await evaluate('latest.address')===walletAddress)throw Error('Import did not switch the active address');
  if(!await evaluate('latest.mode==="STOP" && !document.getElementById("importPhrase").value && !document.getElementById("importPassword").value'))throw Error('Import left activity or secret fields');
  if(fs.readdirSync(path.join(temp,'restored')).some(n=>n.includes('backup')||n.startsWith('.qday-')))throw Error('Import retained an automatic backup');
  await evaluate(`document.getElementById('openImport').click();document.getElementById('importPhrase').value=${JSON.stringify(recoveryWords)};document.getElementById('importPassword').value='final-test-password';document.getElementById('importConfirm').value='final-test-password';document.getElementById('importForm').requestSubmit()`);
  await until(()=>evaluate(`!busy && latest.address===${JSON.stringify(walletAddress)}`),'return to previous wallet using saved seed phrase');
  await evaluate('document.getElementById("lock").click();document.getElementById("confirmLock").click()');
  await until(()=>evaluate('!busy && document.getElementById("unlock").open'),'final wallet lock');
  await evaluate('document.getElementById("quitLocked").click()');
  await until(()=>restored.exitCode===0,'restored launcher graceful exit');
  if(errors.length)throw Error('Browser exceptions: '+errors.join(', '));
  const report={result:'PASS',checks:['genesis inscription committed beside fixed consensus data','pre-genesis mining button disabled with a local countdown','automatic launcher authentication','one-use URL removed','separate browser credential','embedded artwork and fonts','modal onboarding with dimmed backdrop and stable dashboard','mobile onboarding without overflow','invalid recovery checksum and password mismatch rejected inside modal','24-word encrypted wallet creation','explicit plaintext recovery download','backup acknowledgement clears secret fields','mainnet parameters and native CPU mining','confirmed funding from genesis premine','functional navigation','receive address','transfer review and cancel','signed transfer','invalid proof rejected','known test solution rejected by mainnet','live P2P and router diagnostics','STOP, lock and unlock modal','all mobile views without horizontal overflow','EXIT QDAY closes launcher and native node','recovery words restore same address with different password on fresh node','seed phrase export requires password reauthentication','explicit seed phrase copy and download','settings import replaces the active wallet without automatic backup','invalid import preserves current keys','saved seed phrase returns to the previous address','cancel and successful import clear secret fields','all existing font sizes increased by 2px','EXIT QDAY stays in the top right while scrolling','no browser exceptions']};
  report.checks.push('open/closed lock icons follow actual wallet state','lock confirmation and cancel, followed immediately by mandatory unlock screen','locked content is hidden and inert to pointer, keyboard and navigation','Escape, backdrop clicks, wrong passwords and page reload cannot dismiss the lock screen','external lock clears an open seed phrase dialog','EXIT QDAY works from the lock screen','disabled mining explains its cause','top bar fits 320, 390, 768 and 1440 pixels','index catchup retains labelled balance while real zero and wallet/denomination changes still update','manual peer validation and connection in Settings','restart confirmation, new native PID, automatic browser launch and same locked wallet');
  report.checks.push('locked screen accepts any valid QDAY seed phrase','locked import warns before replacing the wallet and retains no backup','invalid locked import preserves encrypted keys','locked seed import fits 320 and 390 pixel screens with reachable scrolling actions');
  report.checks.push('Auto and Custom fee entry, exact total and signed custom-fee send','burn fee review rejects negative values and shows the exact total','proof fee minimum and denomination changes reset fee selection','custom fee controls fit 320 and 390 pixel screens','completed sends reset fee selection to Auto','background errors do not overwrite password errors','dismissed background errors stay dismissed','failed status requests time out and reconnect controls recover');
  fs.writeFileSync(path.join(root,'build/browser-report.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
}
main().catch(err=>{console.error(err.message);process.exitCode=1;}).finally(async()=>{if(socket)socket.close();for(const p of processes){p.kill('SIGTERM');}await sleep(700);for(const p of processes){if(p.exitCode===null)p.kill('SIGKILL');}await sleep(150);fs.rmSync(temp,{recursive:true,force:true});});
