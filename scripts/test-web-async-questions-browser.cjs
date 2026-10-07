// Isolated regression for async question delivery and answer-before-resume.
// NODE_PATH=/path/to/node_modules node scripts/test-web-async-questions-browser.cjs
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path');
const {execFileSync,spawn}=require('node:child_process');
const {chromium}=require('playwright');
const source=path.resolve(__dirname,'..'), out=process.env.PELLETS_BROWSER_ARTIFACTS || fs.mkdtempSync(path.join(os.tmpdir(),'pellets-question-evidence-')); fs.mkdirSync(out,{recursive:true});
const temp=fs.mkdtempSync(path.join(os.tmpdir(),'pellets-question-repro-'));
const binary=path.join(temp,'pl'),peer=path.join(temp,'codex'),repo=path.join(temp,'repro');
const env={...process.env,PATH:temp+path.delimiter+process.env.PATH,PELLETS_CODEX_EXECUTABLE:peer,PELLETS_SUPERVISOR_PEER:'1'};
for(const k of ['GIT_DIR','GIT_WORK_TREE','GIT_COMMON_DIR','GIT_INDEX_FILE'])delete env[k];
let server,browser;const results={temp,cases:[]};
const cli=(...a)=>JSON.parse(execFileSync(binary,['--json',...a],{cwd:repo,env,encoding:'utf8'})).data;
async function until(f){for(let i=0;i<300;i++){if(await f())return;await new Promise(r=>setTimeout(r,100));}throw Error('Timed out');}
(async()=>{
 if(process.env.PELLETS_QUESTION_BASELINE) fs.copyFileSync(process.env.PELLETS_QUESTION_BASELINE,binary);
 else execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:source,env});
 execFileSync('go',['test','-c','-o',peer,'./internal/app'],{cwd:source,env});
 fs.mkdirSync(repo);
 const git=(...a)=>execFileSync('git',a,{cwd:repo,env});
 git('init','-q');git('config','user.name','Test');git('config','user.email','test@example.invalid');git('config','commit.gpgSign','false');git('commit','--allow-empty','-qm','initial');
 fs.appendFileSync(path.join(repo,'.git/info/exclude'),'\n/fake-*\n/.agents/\n');
 execFileSync(binary,['--help'],{cwd:repo,env});execFileSync(binary,['server','--help'],{cwd:repo,env});
 results.init=cli('init-db');cli('add','Reproduce blocked question');cli('skill','install','--scope','repo','--agent','codex','--yes');
 fs.writeFileSync(path.join(repo,'fake-mode'),'schedule_async_question');
 server=spawn(binary,['server','--port','0','--no-open'],{cwd:repo,env});
 const origin=await new Promise((resolve,reject)=>{let s='';server.stdout.on('data',d=>{s+=d;if(s.includes('\n'))resolve(s.split('\n')[0].trim());});server.on('error',reject);});
 results.origin=origin;results.serverPID=server.pid;console.log('Isolated server',origin,'fixture',temp);
 browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL ? {channel:process.env.PLAYWRIGHT_CHANNEL} : {})});
 const page=await browser.newPage({viewport:{width:1280,height:1000}});page.setDefaultTimeout(15000);
 const goto=async()=>{await page.goto(origin+'/projects/repro/tasks?workspace=1');if(!await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();};
 const state=async s=>until(async()=>await page.locator('.execution-state-label').textContent()===s);
 const shot=async name=>page.locator('#right-panel').screenshot({path:path.join(out,name+'.png')});
 await goto();await page.getByRole('button',{name:'▷ Start next',exact:true}).click();if(process.env.PELLETS_QUESTION_BASELINE) {await state('Needs attention');await shot('before');return;}
 await state('Waiting for input');
 const answer=page.locator('.run-interaction textarea');
 await page.getByRole('button',{name:'Skip Windows verification',exact:true}).click();
 assert.equal(await answer.inputValue(),'Skip Windows verification');
 assert.equal(await page.locator('.run-follow-up').count(),0);
 // Selecting an option is not submission or permission. The turn stays idle.
 await page.reload();await state('Waiting for input');
 await shot('async-question');await shot('after');
 await page.setViewportSize({width:390,height:844});await answer.scrollIntoViewIfNeeded();await page.screenshot({path:path.join(out,'async-question-phone.png')});await page.setViewportSize({width:1280,height:1000});
 for (const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy']) {
   await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);
   await page.setViewportSize({width:1092,height:900});
   await answer.scrollIntoViewIfNeeded();
   await shot('question-'+theme);
   assert.equal(await page.getByRole('button',{name:'Skip Windows verification',exact:true}).isVisible(),true);
 }
 await page.evaluate(()=>window.Workbench.applyTheme('gruvbox-light'));
 await page.setViewportSize({width:1280,height:1000});
 await page.getByRole('button',{name:'Stop now',exact:true}).click();await state('Interrupted');
 await page.getByRole('button',{name:'Send and resume',exact:true}).waitFor();
 assert.equal(await page.getByRole('button',{name:'Resume',exact:true}).count(),0);
 // Restart only our child, then verify the saved question still offers choices.
 {const done=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await done;}
 server=spawn(binary,['server','--port','0','--no-open'],{cwd:repo,env});
 const restarted=await new Promise((resolve,reject)=>{let s='';server.stdout.on('data',d=>{s+=d;if(s.includes('\n'))resolve(s.split('\n')[0].trim());});server.on('error',reject);});
 await page.goto(restarted+'/projects/repro/tasks?workspace=1');
 await page.getByRole('button',{name:'Skip Windows verification',exact:true}).click();
 await shot('async-question-restarted');
 await page.getByRole('button',{name:'Send and resume',exact:true}).click();await state('Finished');
 const events=fs.readFileSync(path.join(repo,'fake-events.jsonl'),'utf8').trim().split('\n').map(JSON.parse);
 const starts=events.filter(e=>e.method==='turn/start');
 assert.equal(starts.length,2);assert.match(starts[1].params.input[0].text,/User answers.*[\s\S]*Skip Windows verification/);
 results.cases.push({case:'async choices persist across completed turn and server restart',passed:true});
 cli('add','Stopped run with a plain-text explanation');fs.writeFileSync(path.join(repo,'fake-mode'),'schedule_turn_only');
 await page.goto(restarted+'/projects/repro/tasks?workspace=1');await page.getByRole('button',{name:'▷ Start next',exact:true}).click();await state('Needs attention');
 await page.locator('textarea[name="resume_message"]').fill('Do not upload anything. Verify locally only.');
 await shot('stopped-with-message');
 fs.writeFileSync(path.join(repo,'fake-mode'),'schedule_noop');await page.getByRole('button',{name:'Send and resume',exact:true}).click();await state('Finished');
 const last=fs.readFileSync(path.join(repo,'fake-events.jsonl'),'utf8').trim().split('\n').map(JSON.parse).filter(e=>e.method==='turn/start').at(-1);
 assert.match(last.params.input[0].text,/Do not upload anything. Verify locally only./);
 results.cases.push({case:'generic stopped run receives instructions in first resumed turn',passed:true});
 console.log(JSON.stringify(results,null,2));
})().catch(e=>{results.error=String(e.stack);console.error(e);process.exitCode=1;}).finally(async()=>{
 if(browser)await browser.close();
 if(server&&server.exitCode===null&&server.signalCode===null){const done=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await done;}
 fs.writeFileSync(path.join(out,process.env.PELLETS_QUESTION_BASELINE ? 'baseline-results.json' : 'results.json'),JSON.stringify(results,null,2));
});
