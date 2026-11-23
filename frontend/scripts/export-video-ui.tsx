// ARCHIVED COMPONENT STUDY: this assembles an illustrative shell, NOT native
// Nexus screenshots. The user rejected that treatment for the launch film.
// Do not run this to produce the replacement film; see videos/nexus-native-study
// and its capture provenance. Retained only to reproduce the earlier draft.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { GitFork, Search, Share2, ChevronDown, ArrowUp, Check } from 'lucide-react'
import { ChatMessage } from '../src/components/ChatMessage'
import { ChatDiff } from '../src/components/ChatDiff'
import { NexusBrand } from '../src/components/NexusBrand'

const project = resolve('../videos/nexus-launch')
const css = (readFileSync('src/conversation.css', 'utf8') + readFileSync('src/landing.css', 'utf8')).replace(/\btransition\s*:[^;{}]+;/g, '')
const icon = (component: typeof GitFork, size = 36) => renderToStaticMarkup(createElement(component, { size }))
const brand = renderToStaticMarkup(createElement(NexusBrand)).replaceAll('/brand/nexus-mark.svg', 'assets/nexus-mark.svg')
const lightBrand = brand.replaceAll('assets/nexus-mark.svg', 'assets/nexus-mark-on-light.svg')
const message = (id: number, role: string, text: string, harness = 'Codex', match = '', tools?: unknown) => renderToStaticMarkup(createElement(ChatMessage, { harness, match, turn: { idx: id, role, text_preview: text, tool_calls: tools } }))
const diff = renderToStaticMarkup(createElement(ChatDiff, { path: 'src/session.ts', diff: '@@ -13 +13 @@\n- loadMessages()\n+ loadSharedContext()' })).replaceAll('class="diff-line-number"', 'class="diff-line-number" data-layout-ignore="true"')
const tool = message(912, 'assistant', '', 'Codex', '', [{name:'shell', command:'npm test -- session', output:'Tests passed.'}]).replaceAll('<details class="chat-tool">','<details class="chat-tool" open>')
const cursor = (id: string) => `<svg id="${id}" class="pointer" viewBox="0 0 64 88" aria-hidden="true"><path d="M8 5 L54 43 L34 47 L45 75 L30 82 L19 53 L8 68 Z" fill="#f4f1e9" stroke="#151916" stroke-width="4"/></svg>`
const note = '<p class="fixture-note">Nexus components · illustrative session</p>'
const capturedThread = (prefix: string, idx: number) => `<div id="${prefix}-scroll" class="captured-scroll"><div class="captured-source">${message(idx,'assistant','The decision is in the other chat.','Cursor')}</div><div class="captured-patch">${diff}</div><div class="captured-latest" data-layout-ignore="true">${message(idx+1,'user','Where did we leave off?')}</div></div>`
const shell = (id: string, content: string, harness = 'Codex') => `<div id="${id}" class="app-surface">
  <header class="app-bar"><span>${brand}</span><span>Session restore flow</span><span class="app-actions">${icon(Search)}${icon(GitFork)}<span class="share-control">${icon(Share2)} Share</span></span></header>
  <div class="app-tabs"><span class="selected">Chat</span><span>Activity</span><span>Memory</span></div>
  <main class="app-thread">${content}</main>
  <div class="app-composer"><span class="harness-control">${harness === 'Cursor' && id === 'gather-app' ? '<span class="harness-before">Cursor</span><span class="harness-after">Codex</span>' : harness} ${icon(ChevronDown, 24)}</span><span>${harness === 'Cursor' ? 'Continue the restore work.' : 'Message the agent…'}</span><span class="send-control">${icon(ArrowUp,32)}</span></div>
</div>`

const base = `${css}
#root{position:absolute;inset:0;overflow:hidden;font-family:Inter,sans-serif;color:#f4f1e9;--chat-fg:#f4f1e9;--chat-dim:#b9bfb3;--chat-line:#4e564b}
*{box-sizing:border-box;margin:0}button,select,input{font-family:inherit}.ground{position:absolute;inset:0;background:#f4f1e9}.world{position:absolute;inset:0;transform-origin:50% 50%}.shot{position:absolute;inset:0}.clip{position:absolute;inset:0}
.app-surface{position:absolute;left:160px;top:115px;width:1600px;height:850px;border-radius:32px;background:#20231f;border:1px solid #57604f;box-shadow:0 28px 70px #15191624;overflow:hidden}
.app-bar{height:112px;padding:28px 40px;display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid #495043;font-size:30px}.app-bar .nexus-brand{font-size:36px;gap:12px}.app-bar .nexus-brand img{width:40px;height:40px}.app-actions{display:flex;align-items:center;gap:26px}.share-control{display:flex;align-items:center;gap:10px}
.app-tabs{display:flex;gap:40px;height:72px;padding:22px 55px;font-size:30px;color:#acb7a4}.app-tabs .selected{color:#f4f1e9}.app-thread{position:absolute;left:100px;right:100px;top:215px}.app-composer{position:absolute;left:45px;right:45px;bottom:30px;height:115px;border:1px solid #5f6857;border-radius:24px;padding:24px 30px;display:flex;align-items:center;gap:28px;color:#aeb7a5;font-size:30px;background:#30362c}.harness-control{display:flex;align-items:center;gap:15px;color:#f4f1e9}.send-control{margin-left:auto;width:55px;height:55px;display:grid;place-items:center;background:#e8bb67;color:#1b2118;border-radius:17px}
.chat-turn{margin:0 0 28px}.chat-turn.is-user{max-width:88%;margin-left:auto;padding:22px 30px;background:#363e30;border-radius:24px}.chat-turn-role{margin-bottom:12px}.chat-turn-role>span{font-size:30px;color:#c4d1b6}.chat-turn-role button,.chat-diff>header button{display:none}.chat-prose{font-size:55px;line-height:1.26}.chat-prose p{margin:10px 0}.chat-tool{background:#252c21;border-radius:20px}.chat-tool summary{font-size:36px;padding:20px 28px}.chat-tool summary>span{font-size:32px}.chat-tool summary>code{display:none}.chat-tool-details{padding:12px 26px 22px}.chat-tool-details small{font-size:28px;display:block;margin:8px 0}.chat-tool-details pre{font:64px/1.3 'IBM Plex Mono',monospace;max-height:none;padding:14px 18px;background:#171c15}.chat-tool-list{margin-top:0}
.chat-diff{margin:0;border-radius:22px;background:#20231f}.chat-diff>header{padding:24px 32px;font-size:36px}.chat-diff-stats{font-size:32px}.chat-diff-body{font:68px/1.55 'IBM Plex Mono',monospace;max-height:none;padding:20px 0}.diff-line-number{font-size:27px;width:54px;margin-right:12px}.chat-diff>header svg{width:30px;height:30px}.chat-diff-body code>span{padding:0 28px}
.fixture-note{position:absolute;left:192px;bottom:52px;color:#59644e;font-size:28px}.pointer{position:absolute;width:78px;height:105px;z-index:20}.editorial{position:absolute;font-size:140px;line-height:1.02;font-weight:500;letter-spacing:-.065em;color:#1b2118}.macro-message{position:absolute;left:192px;top:310px;width:1536px}.macro-message .chat-prose{font-size:82px}.macro-message .chat-turn-role>span{font-size:36px}.macro-message .chat-turn.is-user{max-width:100%}.macro-panel{position:absolute;left:192px;top:240px;width:1536px;padding:44px;border:1px solid #647057;border-radius:28px;background:#20231f}.compact-heading{font-size:42px;color:#bdcbb0;margin-bottom:35px}.mini-origin{font-size:30px;color:#b1c39d;display:flex;align-items:center;gap:14px;margin-bottom:36px}
`
const continuityCss = '.captured-source .chat-prose{font-size:48px}.captured-patch .chat-diff-body{font-size:40px}.captured-patch .chat-diff>header{padding:14px 25px}.captured-latest{margin-top:35px}.captured-scroll{position:relative}#gather-app .app-thread,#scatter-frame .app-thread{height:470px;overflow:hidden}'
function scene(id: string, markup: string, styles: string, motion: string) {
  writeFileSync(resolve(project, `compositions/${id}.html`), `<template><style>${base}${continuityCss}${styles}</style><div id="root" data-composition-id="${id}" data-width="1920" data-height="1080"><div class="ground"></div>${markup}${note}</div><script>const tl=gsap.timeline({paused:true});${motion}window.__timelines['${id}']=tl;</script></template>`)
}

// Opening: source fragments become a product, not six unrelated title slides.
scene('scatter', `
<div id="scatter-world" class="world">
  <div id="scatter-anchor" class="fragment">${message(901,'assistant','The decision is in the other chat.','Cursor')}</div>
  <div id="scatter-patch" class="fragment">${diff}</div>
  <div id="scatter-question" class="fragment">${message(902,'user','Where did we leave off?')}</div>
</div><h1 id="scatter-copy" class="editorial">Keep going.</h1>
<div data-layout-ignore="true">${shell('scatter-frame',capturedThread('scatter-captured',919),'Cursor')}</div>
`, `
.fragment{position:absolute;background:#20231f;border-radius:26px;padding:40px;border:1px solid #647057}#scatter-anchor{left:190px;top:210px;width:1230px}#scatter-anchor .chat-prose{font-size:80px}#scatter-patch{left:720px;top:550px;width:1040px;padding:0}#scatter-patch .chat-diff-body{font-size:46px}#scatter-question{left:210px;top:650px;width:900px}#scatter-question .chat-prose{font-size:60px}#scatter-copy{left:192px;top:145px}#scatter-frame{background:#20231f}
#scatter-patch{left:1000px;top:420px;width:740px}#scatter-patch .chat-diff-body{font-size:34px}#scatter-question{width:690px}
`, `
tl.fromTo('#scatter-world',{scale:1.35,x:250,y:110},{scale:1,x:0,y:0,duration:.8,ease:'expo.out'},0);
tl.fromTo('#scatter-patch',{x:850,opacity:0},{x:0,opacity:1,duration:.6,ease:'expo.out'},1.45);
tl.fromTo('#scatter-question',{x:-850,opacity:0},{x:0,opacity:1,duration:.6,ease:'expo.out'},2.8);
tl.to('#scatter-anchor',{y:-600,duration:.6,ease:'power2.inOut'},3.65);tl.to('#scatter-anchor',{opacity:0,duration:.15},3.65);
tl.to('#scatter-patch',{x:150,y:-130,duration:.6,ease:'power2.inOut'},3.65);
tl.to('#scatter-question',{x:-70,y:110,duration:.6,ease:'power2.inOut'},3.65);
tl.fromTo('#scatter-copy',{clipPath:'inset(0 100% 0 0)'},{clipPath:'inset(0 0% 0 0)',duration:.35,ease:'power4.out'},4.45);
tl.to('#scatter-world',{scale:.72,x:0,y:0,opacity:0,duration:.55,ease:'power4.in'},6.65);
tl.to('#scatter-copy',{y:-110,opacity:0,duration:.3,ease:'power4.in'},6.9);
tl.fromTo('#scatter-frame',{scale:.72,clipPath:'inset(50% 0 50% 0)',opacity:0},{scale:1,clipPath:'inset(0% 0 0% 0)',opacity:1,duration:.7,ease:'power4.out'},7.3);`)

scene('gather', `
<div id="gather-world" class="world">${shell('gather-app',capturedThread('gather-captured',921),'Cursor')}
<div id="gather-menu"><span>Cursor</span><strong>Codex ${icon(Check,32)}</strong><span>OpenCode</span><span>Claude Code</span></div></div>
<div id="gather-macro" class="macro-message">${message(905,'assistant','Context loaded.\n\nThe patch is ready.')}</div>
<h2 id="gather-copy" class="editorial">Another agent.<br>Same work.</h2>${cursor('gather-pointer')}
`, `
#gather-menu{position:absolute;left:205px;top:565px;width:400px;background:#30372b;border:1px solid #758266;border-radius:18px;padding:12px;font-size:36px;box-shadow:0 20px 50px #0004}#gather-menu>*{display:flex;justify-content:space-between;padding:15px 22px;font-weight:400}#gather-menu strong{background:#525f45;border-radius:10px}#gather-copy{left:192px;top:150px;font-size:112px;color:#f4f1e9}#gather-macro{top:455px}#gather-macro .chat-prose{font-size:86px}#gather-pointer{left:1610px;top:890px}.harness-control{position:relative}.harness-before,.harness-after{width:98px}.harness-after{position:absolute;left:0}
`, `
tl.fromTo('#gather-world',{scale:1,x:0,y:0},{scale:1.35,x:310,y:-140,duration:.65,ease:'power2.inOut'},1.1);
tl.fromTo('#gather-pointer',{x:0,y:0,opacity:0},{x:-1290,y:-50,opacity:1,duration:.65,ease:'expo.out'},1.9);
tl.to('#gather-pointer',{scale:.84,duration:.1},2.6);tl.to('#gather-pointer',{scale:1,duration:.16},2.7);
tl.fromTo('#gather-menu',{y:45,opacity:0},{y:0,opacity:1,duration:.25,ease:'power4.out'},2.7);
tl.to('#gather-pointer',{x:-1260,y:-310,duration:.4,ease:'power2.inOut'},3.55);
tl.to('#gather-pointer',{scale:.84,duration:.1},4);tl.to('#gather-pointer',{scale:1,duration:.16},4.1);
tl.to('#gather-app .harness-before',{opacity:0,duration:.05},4.1);
tl.fromTo('#gather-app .harness-after',{opacity:0},{opacity:1,duration:.1},4.1);
tl.to('#gather-menu',{opacity:0,duration:.18,ease:'power4.in'},4.1);
tl.to('#gather-world',{scale:1,x:0,y:0,duration:.55,ease:'power2.inOut'},4.2);
tl.to('#gather-captured-scroll',{y:-325,duration:.55,ease:'power2.inOut'},4.2);
tl.to('#gather-pointer',{x:30,y:-15,duration:.55,ease:'power2.inOut'},4.65);
tl.to('#gather-pointer',{scale:.84,duration:.1},5.25);tl.to('#gather-pointer',{scale:1,duration:.16},5.35);
tl.to('#gather-pointer',{opacity:0,duration:.2},5.45);
tl.to('#gather-world',{x:-650,opacity:0,duration:.35,ease:'power4.in'},5.5);
tl.fromTo('.ground',{backgroundColor:'#f4f1e9'},{backgroundColor:'#20231f',duration:.45,ease:'power2.inOut'},5.5);
tl.to('.fixture-note',{color:'#b7c4ac',duration:.45},5.5);
tl.fromTo('#gather-copy',{x:1100,opacity:0},{x:0,opacity:1,duration:.65,ease:'power4.out'},5.85);
tl.fromTo('#gather-macro',{x:1100,opacity:0},{x:0,opacity:1,duration:.65,ease:'power4.out'},5.85);
tl.fromTo('#gather-macro .chat-prose p',{clipPath:'inset(0 100% 0 0)'},{clipPath:'inset(0 0% 0 0)',duration:.55,stagger:.65,ease:'none'},6.6);
tl.to('#gather-copy',{opacity:0,duration:.25,ease:'power4.in'},9.45);
tl.to('#gather-macro',{y:-145,duration:.5,ease:'power2.inOut'},9.45);`)

scene('proof', `
<div id="proof-reply-shot" class="clip" data-start="0" data-duration="2.8" data-track-index="1"><div id="proof-reply" class="macro-message">${message(906,'assistant','Context loaded.\n\nThe patch is ready.')}</div></div>
<div id="proof-tool-shot" class="clip" data-start="2.8" data-duration="6.25" data-track-index="2"><div id="proof-tool" class="macro-panel"><p class="compact-heading">Recorded activity</p>${tool}</div></div>
<div id="proof-diff-shot" class="clip" data-start="9.05" data-duration="4.95" data-track-index="3"><div id="proof-diff" class="macro-panel">${diff}</div></div>
<h2 id="proof-copy" class="editorial">See what changed.</h2>${cursor('proof-pointer')}
`, `
.ground{background:#20231f}.fixture-note{color:#b7c4ac}#proof-reply{top:310px}#proof-tool{top:215px}#proof-tool .chat-tool{border:0}#proof-tool .chat-tool summary{padding-left:0}#proof-diff{padding:0;top:315px}#proof-copy{left:192px;top:110px;font-size:110px;color:#f4f1e9}#proof-pointer{left:1620px;top:350px}
`, `
tl.to('#proof-reply',{y:-1100,duration:.4,ease:'power4.in'},2.4);
tl.fromTo('#proof-tool',{y:1100},{y:0,duration:.65,ease:'power4.out'},2.8);
tl.fromTo('#proof-tool .chat-tool-details',{clipPath:'inset(0 0 100% 0)'},{clipPath:'inset(0 0 0% 0)',duration:.55,ease:'power2.inOut'},4.4);
tl.fromTo('#proof-pointer',{x:0,y:100,opacity:0},{x:-250,y:80,opacity:1,duration:.55,ease:'expo.out'},3.4);
tl.to('#proof-pointer',{scale:.84,duration:.1},4.3);tl.to('#proof-pointer',{scale:1,duration:.16},4.4);
tl.to('#proof-pointer',{opacity:0,duration:.2},4.8);
tl.fromTo('#proof-tool .chat-tool-details pre:last-child',{opacity:0},{opacity:1,duration:.25},6.2);
tl.to('#proof-tool',{y:-1100,duration:.35,ease:'power4.in'},8.7);
tl.fromTo('#proof-diff',{y:1100},{y:0,duration:.7,ease:'power4.out'},9.05);
tl.fromTo('#proof-copy',{clipPath:'inset(0 100% 0 0)'},{clipPath:'inset(0 0% 0 0)',duration:.4,ease:'power4.out'},9.5);
tl.fromTo('#proof-diff .diff-added',{clipPath:'inset(0 100% 0 0)'},{clipPath:'inset(0 0% 0 0)',duration:.5,ease:'none'},10.2);
tl.to('#proof-copy',{opacity:0,duration:.25},13.45);
tl.to('#proof-diff',{y:-100,duration:.55,ease:'power2.inOut'},13.4);`)

scene('handoff', `
<div id="handoff-world" class="world">${shell('handoff-app',`<div id="handoff-small-patch">${diff}</div>`)}<div id="handoff-dialog"><h3>Share conversation</h3><p>Teammate</p><div class="person"><span>Maya · Member</span>${icon(ChevronDown)}</div><button id="handoff-share-button">Share with teammate</button></div></div>
<div id="handoff-carry-shot" class="clip" data-start="0" data-duration=".85" data-track-index="1"><div id="handoff-carry" class="macro-panel" data-layout-ignore="true">${diff}</div></div>
<div id="handoff-next-shot" class="clip" data-start="6.85" data-duration="5.15" data-track-index="2"><div id="handoff-next" class="macro-panel"><div class="mini-origin">${icon(GitFork)}Session restore flow · shared context</div>${message(913,'assistant','Continuing the restore flow.','OpenCode')}</div></div>
<h2 id="handoff-copy" class="editorial">Over to you.</h2>${cursor('handoff-pointer')}
`, `
.ground{background:#d9dfce}#handoff-carry{top:215px;padding:0}#handoff-dialog{position:absolute;left:530px;top:285px;width:860px;padding:45px;background:#30372b;border:1px solid #748366;border-radius:30px;box-shadow:0 20px 100px #0005;color:#f4f1e9}#handoff-dialog h3{font-size:56px;font-weight:500;margin-bottom:35px}#handoff-dialog p{font-size:32px;color:#bfcbb3;margin-bottom:14px}.person{display:flex;align-items:center;justify-content:space-between;background:#414b38;padding:25px;font-size:42px;border-radius:14px}#handoff-share-button{margin-top:35px;padding:24px 30px;font-size:36px;background:#e8bb67;color:#1b2118;border:0;border-radius:15px}#handoff-small-patch .chat-diff-body{font-size:52px}#handoff-next{top:380px}#handoff-next .chat-prose{font-size:78px}#handoff-copy{left:192px;top:160px;font-size:142px}#handoff-pointer{left:1650px;top:300px}
`, `
tl.fromTo('#handoff-world',{scale:1,opacity:0},{scale:1,opacity:1,duration:.05},0);
tl.fromTo('#handoff-carry',{scale:1,x:0,y:0},{scale:.82,x:0,y:65,opacity:0,duration:.7,ease:'power2.inOut'},.15);
tl.fromTo('#handoff-dialog',{y:65,opacity:0},{y:0,opacity:1,duration:.55,ease:'expo.out'},2.6);
tl.to('#handoff-small-patch',{opacity:0,duration:.15},2.4);tl.to('#handoff-small-patch',{opacity:1,duration:.15},5.65);
tl.fromTo('#handoff-pointer',{x:100,y:0,opacity:0},{x:-20,y:-140,opacity:1,duration:.6,ease:'expo.out'},1.4);
tl.to('#handoff-pointer',{scale:.84,duration:.1},2.5);tl.to('#handoff-pointer',{scale:1,duration:.16},2.6);
tl.to('#handoff-pointer',{x:-850,y:345,duration:.65,ease:'power2.inOut'},4.35);
tl.to('#handoff-pointer',{scale:.84,duration:.1},5.05);tl.to('#handoff-pointer',{scale:1,duration:.16},5.15);
tl.to('#handoff-dialog',{scale:.94,opacity:0,duration:.3,ease:'power4.in'},5.3);
tl.to('#handoff-pointer',{opacity:0,duration:.18},5.4);
tl.to('#handoff-world',{x:750,opacity:0,duration:.35,ease:'power4.in'},6.5);
tl.fromTo('#handoff-next',{x:-1900},{x:0,duration:.7,ease:'power4.out'},6.85);
tl.fromTo('#handoff-copy',{clipPath:'inset(0 100% 0 0)'},{clipPath:'inset(0 0% 0 0)',duration:.4,ease:'power4.out'},7.3);
tl.fromTo('#handoff-next .chat-prose',{clipPath:'inset(0 100% 0 0)'},{clipPath:'inset(0 0% 0 0)',duration:.6,ease:'none'},8.2);
tl.to('#handoff-copy',{opacity:0,duration:.25},11.5);
tl.to('#handoff-next',{y:-100,duration:.5,ease:'power2.inOut'},11.45);`)

scene('remember', `
<div id="remember-origin-shot" class="clip" data-start="0" data-duration="1.1" data-track-index="1"><div id="remember-origin" class="macro-panel" data-layout-ignore="true"><div class="mini-origin">${icon(GitFork)}Session restore flow · shared context</div>${message(914,'assistant','Continuing the restore flow.','OpenCode')}</div></div>
<div id="remember-search">${icon(Search,48)}<span>Find across this conversation</span><strong>restore</strong></div>
<div id="remember-decision" class="macro-panel">${message(915,'assistant','Keep restore history\nand code together.','Codex','restore')}</div>
<div id="remember-continuation-shot" class="clip" data-start="5.9" data-duration="4.1" data-track-index="2"><div id="remember-continuation" class="macro-panel">${message(916,'assistant','Continuing from that decision.','OpenCode')}</div></div>
<h2 id="remember-copy" class="editorial">No starting over.</h2>${cursor('remember-pointer')}
`, `
.ground{background:#d9dfce}#remember-origin{top:280px}#remember-origin .chat-prose{font-size:78px}#remember-search{position:absolute;left:192px;top:170px;width:1536px;padding:35px 40px;background:#f4f1e9;border:1px solid #b0bd9f;border-radius:24px;display:flex;align-items:center;gap:30px;color:#3c4d30;font-size:34px}#remember-search strong{font-size:64px;color:#1b2118;font-weight:500;margin-left:auto}#remember-decision{top:390px}#remember-decision .chat-prose{font-size:78px}#remember-decision .chat-turn.is-match{outline:0}#remember-continuation{top:390px}#remember-continuation .chat-prose{font-size:78px}#remember-copy{left:192px;top:160px;font-size:130px}#remember-pointer{left:1560px;top:280px}
`, `
tl.to('#remember-origin',{y:-1000,duration:.4,ease:'power4.in'},.7);
tl.fromTo('#remember-search',{y:900},{y:0,duration:.65,ease:'power4.out'},1.1);
tl.fromTo('#remember-search strong',{clipPath:'inset(0 100% 0 0)'},{clipPath:'inset(0 0% 0 0)',duration:.5,ease:'none'},2.1);
tl.fromTo('#remember-decision',{y:150,opacity:0},{y:0,opacity:1,duration:.5,ease:'expo.out'},2.75);
tl.fromTo('#remember-pointer',{x:0,y:0,opacity:0},{x:-280,y:270,opacity:1,duration:.55,ease:'expo.out'},4.3);
tl.to('#remember-pointer',{scale:.84,duration:.1},4.95);tl.to('#remember-pointer',{scale:1,duration:.15},5.05);
tl.to('#remember-search',{y:-400,opacity:0,duration:.35,ease:'power4.in'},5.25);
tl.to('#remember-decision',{x:-1900,duration:.35,ease:'power4.in'},5.55);
tl.fromTo('#remember-continuation',{x:1900},{x:0,duration:.7,ease:'power4.out'},5.9);
tl.to('#remember-pointer',{opacity:0,duration:.18},5.25);
tl.fromTo('#remember-copy',{clipPath:'inset(0 100% 0 0)'},{clipPath:'inset(0 0% 0 0)',duration:.4,ease:'power4.out'},6.6);
tl.to('#remember-continuation',{scale:.8,y:40,duration:.55,ease:'power2.inOut'},9.35);
tl.to('#remember-copy',{opacity:0,duration:.25},9.5);`)

scene('close', `
<div id="close-source" class="macro-panel" data-layout-ignore="true">${message(917,'assistant','Continuing from that decision.','OpenCode')}</div>
<div id="close-lockup">${lightBrand}</div><h2 id="close-claim" data-hold="End claim" data-var-text="claim">Different agents.<br>Shared momentum.</h2><div id="close-cta" data-hold="Beta call to action" data-var-text="cta">Nexus Desktop Beta</div><p id="close-platform">Windows · Prerelease</p>
`, `
.ground{background:#d9dfce}#close-source{top:430px}#close-source .chat-prose{font-size:78px}#close-lockup{position:absolute;left:620px;top:220px;width:680px}#close-lockup .nexus-brand{font-size:142px;gap:34px;color:#1b2118}#close-lockup img{width:155px;height:155px}#close-claim{position:absolute;left:240px;top:465px;width:1440px;text-align:center;font-size:88px;line-height:1.05;letter-spacing:-.05em;font-weight:500;color:#1b2118}#close-cta{position:absolute;left:690px;top:755px;width:540px;padding:24px;text-align:center;background:#1f2a19;color:#f4f1e9;border-radius:16px;font-size:40px}#close-platform{position:absolute;left:640px;top:865px;width:640px;text-align:center;color:#526447;font-size:30px}.fixture-note{display:none}
`, `
tl.fromTo('#close-source',{scale:.8,y:0},{scale:.08,y:-150,opacity:0,duration:.65,ease:'power4.in'},0);
tl.fromTo('#close-lockup img',{scale:.25,opacity:0},{scale:1,opacity:1,duration:.65,ease:'expo.out'},.6);
tl.fromTo('#close-lockup span',{clipPath:'inset(0 100% 0 0)'},{clipPath:'inset(0 0% 0 0)',duration:.55,ease:'power4.out'},1.05);
tl.fromTo('#close-claim',{opacity:0,filter:'blur(8px)'},{opacity:1,filter:'blur(0px)',duration:.3,ease:'power4.out'},1.8);
tl.fromTo('#close-cta,#close-platform',{opacity:0},{opacity:1,duration:.25,ease:'none'},2.5);`)

console.log('Exported six product-led scenes with continuous UI objects and macro/whole-workspace reframing.')
