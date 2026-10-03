import { chromium } from '@playwright/test';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
// Read-only authenticated audit: no saved cookies, traces or API payloads.
// See e2e-scenarios/mobile-layout-audit.md for configuration and assertions.
const required = ['AUDIT_BASE_URL', 'AUDIT_PROJECT_ID', 'AUDIT_WORKSPACE_SLUG', 'AUDIT_TASK_ID', 'E2E_USER_EMAIL', 'E2E_USER_PASSWORD'];
for (const name of required) if (!process.env[name]) throw new Error(`Missing ${name}`);
const root = process.env.AUDIT_OUTPUT_DIR || 'mobile-layout-artifacts';
const base = process.env.AUDIT_BASE_URL;
const browser = await chromium.launch({headless:true, ...(process.env.AUDIT_BROWSER_CHANNEL ? {channel:process.env.AUDIT_BROWSER_CHANNEL} : {})});
async function request(label, operation) {
  try { return await operation(); }
  catch { throw new Error(`${label}: transport failed (request details suppressed)`); }
}
const violations = [];
const out=[];
await mkdir(root,{recursive:true});
try {
  const context=await browser.newContext({viewport:{width:393,height:852},isMobile:false,hasTouch:true,deviceScaleFactor:1,serviceWorkers:'block'});
  // Optional candidate assets on the real API origin, useful when a local
  // reverse proxy cannot keep its upstream TLS connection alive.
  if(process.env.AUDIT_ASSET_DIR){
    const {resolve,extname,sep}=await import('node:path');
    const assetRoot=resolve(process.env.AUDIT_ASSET_DIR);
    const mime={'.html':'text/html','.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml','.png':'image/png','.woff2':'font/woff2','.json':'application/json','.webmanifest':'application/manifest+json'};
    await context.route('**/*',async route=>{
      const url=new URL(route.request().url());
      if(url.origin!==new URL(base).origin||url.pathname.startsWith('/api/'))return route.continue();
      const path=resolve(assetRoot,route.request().resourceType()==='document'?'index.html':url.pathname.slice(1));
      if(!path.startsWith(assetRoot+sep))throw new Error('Asset path outside candidate directory');
      try{await route.fulfill({body:await readFile(path),contentType:mime[extname(path)]||'application/octet-stream'});}
      catch{throw new Error('Candidate asset unavailable (request details suppressed)');}
    });
  }
  const login=await request('login',()=>context.request.post(`${base}/api/v1/auth/login`,{timeout:20000,data:{email:process.env.E2E_USER_EMAIL,password:process.env.E2E_USER_PASSWORD}}));
  if(login.status()!==200)throw new Error(`login status ${login.status()}`);
  const auth=await login.json();
  if(!auth.tokens?.access_token)throw new Error('missing access token');
  const headers={Authorization:`Bearer ${auth.tokens.access_token}`};
  const projectRes=await request('project',()=>context.request.get(`${base}/api/v1/projects/${process.env.AUDIT_PROJECT_ID}`,{headers,timeout:20000}));
  if(projectRes.status()!==200)throw new Error(`project status ${projectRes.status()}`);
  const project=await projectRes.json();
  const docsRes=await request('documents',()=>context.request.get(`${base}/api/v1/projects/${process.env.AUDIT_PROJECT_ID}/documents`,{headers,timeout:20000}));
  if(docsRes.status()!==200)throw new Error(`documents status ${docsRes.status()}`);
  const docs=await docsRes.json();
  const doc=process.env.AUDIT_DOCUMENT_ID ? (docs.items??docs).find(d=>d.id===process.env.AUDIT_DOCUMENT_ID) : (docs.items??docs).find(d=>!d.parent_id)??(docs.items??docs)[0];
  if(!doc?.id)throw new Error('missing document fixture');
  const ws=process.env.AUDIT_WORKSPACE_SLUG;
  const pp=`/w/${ws}/p/${project.slug}`;
  const screens=[['board',pp],['task',`${pp}/t/${process.env.AUDIT_TASK_ID}`],['docs',`${pp}/docs`],['document',`${pp}/docs/${doc.id}`],['analytics',`/w/${ws}/analytics`],['sessions',`/w/${ws}/sessions`],['settings',`/w/${ws}/settings`]];
  const page=await context.newPage();
  const failures=[];
  const pending=new Set();
  page.on('request',r=>{if(new URL(r.url()).pathname.startsWith('/api/v1/'))pending.add(r);});
  page.on('requestfinished',r=>pending.delete(r));
  page.on('requestfailed',r=>{pending.delete(r);if(new URL(r.url()).pathname.startsWith('/api/v1/'))failures.push(`transport ${new URL(r.url()).pathname}`);});
  page.on('response',r=>{if(r.url().includes('/api/v1/')&&r.status()>=400&&!r.url().includes('/auth/refresh'))failures.push(`${r.status()} ${new URL(r.url()).pathname}`);});
  page.on('pageerror',e=>failures.push(`pageerror ${e.message}`));
  const measure=()=>page.evaluate(()=>{
      const visible=e=>{const r=e.getBoundingClientRect();const s=getComputedStyle(e);return r.width>0&&r.height>0&&r.top<innerHeight&&r.bottom>0&&r.left<innerWidth&&r.right>0&&s.visibility!=='hidden'&&s.display!=='none'&&![e,...(()=>{const a=[];let p=e.parentElement;while(p){a.push(p);p=p.parentElement;}return a;})()].some(p=>Number(getComputedStyle(p).opacity)===0);};
      const clipped=e=>{const r=e.getBoundingClientRect();let left=Math.max(0,r.left),right=Math.min(innerWidth,r.right),top=Math.max(0,r.top),bottom=Math.min(innerHeight,r.bottom);for(let p=e.parentElement;p;p=p.parentElement){const s=getComputedStyle(p),a=p.getBoundingClientRect();if(s.overflowX!=='visible'){left=Math.max(left,a.left);right=Math.min(right,a.right);}if(s.overflowY!=='visible'){top=Math.max(top,a.top);bottom=Math.min(bottom,a.bottom);}}return right>left&&bottom>top;};
      const item=e=>{const r=e.getBoundingClientRect();return {surface:e.closest('header')?'header':'main',tag:e.tagName,label:(e.getAttribute('aria-label')||e.getAttribute('title')||e.textContent||'').trim().slice(0,100),class:e.className,x:Math.round(r.x),y:Math.round(r.y),width:Math.round(r.width*10)/10,height:Math.round(r.height*10)/10};};
      return {mainWidth:document.querySelector("main").clientWidth,mainScrollWidth:document.querySelector("main").scrollWidth,innerWidth,scrollWidth:document.documentElement.scrollWidth,overflow:document.documentElement.scrollWidth>innerWidth,theme:document.documentElement.classList.contains('dark')?'dark':'light',headings:[...document.querySelectorAll('h1,h2,h3')].map(e=>e.textContent),bodyText:document.body.innerText.slice(0,1200),smallTargets:[...document.querySelectorAll('button,a,input,select,[role="button"],[role="tab"]')].filter(visible).filter(clipped).filter(e=>{const r=e.getBoundingClientRect();return r.width<44||r.height<44;}).map(item),offscreen:[...document.querySelectorAll('main *')].filter(visible).filter(clipped).filter(e=>e.getBoundingClientRect().right>innerWidth+1).map(item).slice(0,25)};
    });
  for (const width of [393,1440]) {
   await page.setViewportSize({width,height:width===393?852:900});
   for(const theme of ['light','dark']){
   await page.emulateMedia({colorScheme:theme});
   if(page.url()!=='about:blank')await page.evaluate(t=>{localStorage.setItem('theme',t);document.documentElement.classList.toggle('dark',t==='dark');},theme);
   for(const [screen,path] of screens){
    failures.length=0;
    if(page.url()==='about:blank')await page.goto(base+path,{waitUntil:'domcontentloaded'});
    else await page.evaluate(path=>{history.pushState({},'',path);window.dispatchEvent(new PopStateEvent('popstate'));},path);
    await page.waitForTimeout(200);
    await page.locator('header').waitFor({timeout:30000}).catch(async error=>{console.error(`Render failed: ${page.url()}; ${JSON.stringify(failures)}; ${(await page.locator('body').innerText()).slice(0,200)}`);await page.screenshot({path:`${root}/render-failure.png`});throw error;});
    await page.waitForFunction(()=>document.querySelector('main')?.innerText.length>20);
    await page.waitForTimeout(500);
    const deadline=Date.now()+30000;
    while(pending.size){
      if(Date.now()>deadline)throw new Error(`${screen}: API requests still pending (details suppressed)`);
      await page.waitForTimeout(100);
    }
    await page.evaluate(()=>{window.scrollTo(0,0);document.querySelector('main')?.scrollTo(0,0);});
    await page.waitForTimeout(200);
    if(page.url().includes('/login'))throw new Error(`unauthenticated ${screen}`);
    const metric=await measure();
    if(metric.innerWidth!==width||metric.theme!==theme)throw new Error(`viewport/theme mismatch ${screen} ${JSON.stringify({width:metric.innerWidth,theme:metric.theme})}`);
    if(metric.overflow || metric.mainScrollWidth>metric.mainWidth+1) violations.push(`${screen} ${width} ${theme}: unexpected page/main overflow`);
    if(failures.length) violations.push(`${screen} ${width} ${theme}: API/render errors`);
    if(width===393){const targets=screen==='task' ? metric.smallTargets.filter(t=>t.surface==='header') : metric.smallTargets; if(targets.length)violations.push(`${screen} ${width} ${theme}: ${targets.length} targets below 44px`);}
    const filename=`${screen}-${width}-${theme}.png`;
    await page.screenshot({path:`${root}/${filename}`});
    if(width===393&&theme==='light')await page.screenshot({path:`${root}/crop-${screen}.png`,clip:{x:0,y:screen==='task'?300:0,width,height:screen==='task'?440:screen==='document'?400:screen==='docs'?400:300}});
    const costViews=[];
    if(screen==='sessions'){
     for(const [position,label] of [['top','Cost Tracking'],['bottom','Most expensive tasks']]){
      const heading=page.getByText(label,{exact:true});
      if(!await heading.count())throw new Error(`${label} content missing`);
      await heading.evaluate(el=>el.scrollIntoView({block:'start'}));
      await page.waitForTimeout(200);
      const costMetric=await measure();
      if(costMetric.overflow || costMetric.mainScrollWidth>costMetric.mainWidth+1 || (width===393&&costMetric.smallTargets.length))violations.push(`sessions cost ${position} ${width} ${theme}: overflow/small targets`);
      const costFile=`sessions-cost-${position}-${width}-${theme}.png`;
      await page.screenshot({path:`${root}/${costFile}`});
      costViews.push({position,file:costFile,...costMetric});
     }
    }
    out.push({screen,width,theme,url:page.url(),file:filename,...metric,costViews,failures:[...failures]});
    await writeFile(`${root}/metrics.json`,JSON.stringify(out,null,2));
    console.log(`${screen} ${width} ${theme}: scrollWidth=${metric.scrollWidth} innerWidth=${metric.innerWidth} smallTargets=${metric.smallTargets.length} errors=${failures.length}`);
   }
  }
 }
  await context.close();
} finally {await browser.close();}
console.log(`AUDIT: ${out.length}/28 states; ${violations.length} violations`);
await writeFile(`${root}/verdict.json`,JSON.stringify({states:out.length,violations},null,2));
if(violations.length&&process.env.AUDIT_COLLECT_ONLY!=='1')process.exitCode=1;
