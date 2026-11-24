<script lang="ts">
  import { onMount } from 'svelte'
  import { CloseWorkspace, Current, List, NewWindow, RenameWorkspace, Running } from '../wailsjs/go/main/App'
  import { EventsOn } from '../wailsjs/runtime/runtime'
  import type { store } from '../wailsjs/go/models'
  import NewRequest from './NewRequest.svelte'
  import RequestView from './RequestView.svelte'
  import Employees from './Employees.svelte'
  import Projects from './Projects.svelte'
  import Welcome from './Welcome.svelte'

  type Log = { id: string; t: number; role: string; line: string }

  let ws = $state<store.Workspace | null>(null)
  let loaded = $state(false)
  let requests = $state<store.RequestRow[]>([])
  let selected = $state<string | null>(null) // null = new request form, 'employees' = employee screen
  let running = $state<string[]>([])
  let logs = $state<Record<string, Log[]>>({})
  let error = $state('')
  let tick = $state(0) // bumps when a request finishes so the detail reloads

  async function refresh() {
    try {
      ws = (await Current()) ?? null
      loaded = true
      requests = (await List()) ?? []
      running = (await Running()) ?? []
    } catch (e) {
      error = String(e)
    }
  }

  onMount(() => {
    refresh()
    const offLog = EventsOn('log', (l: Log) => {
      logs[l.id] = [...(logs[l.id] ?? []), l]
      refresh() // costs and statuses change with every step
    })
    const offDone = EventsOn('done', () => {
      refresh()
      tick++
    })
    return () => {
      offLog()
      offDone()
    }
  })

  // A workspace was opened in this window (or closed): start fresh.
  function switched() {
    selected = null
    refresh()
  }

  async function closeWs() {
    await CloseWorkspace()
    switched()
  }

  async function rename() {
    const name = prompt('작업공간 이름', ws?.name ?? '')
    if (!name) return
    try {
      await RenameWorkspace(name)
      refresh()
    } catch (e) {
      error = String(e)
    }
  }

  function started(id: string) {
    logs[id] = []
    selected = id
    refresh()
  }

  const cost = (c: number) => '$' + c.toFixed(3)
  const modeName: Record<string, string> = { ask: '질문', plan: '기획', audit: '검토', meeting: '회의', solo: '혼자 개발', team: '팀', review: '팀 + QA' }
  const short = (s: string) => (s.length > 60 ? s.slice(0, 60) + '…' : s)
</script>

{#if !loaded}
  <div></div>
{:else if !ws}
  <main class="solo">
    {#if error}<div class="err">{error}</div>{/if}
    <Welcome onopened={switched} />
  </main>
{:else}
<div class="layout">
  <aside>
    <div class="brand">
      <button class="wsname" onclick={rename} title="이름 바꾸기">{ws.name}</button>
      <div class="wsbtns">
        <button class="small" onclick={() => NewWindow(0)} title="새 창">새 창</button>
        <button class="small" onclick={closeWs} title="시작 화면으로">닫기</button>
      </div>
    </div>
    <div class="folders">
      {#each ws.projects as p (p.id)}<div class="folder" title={p.repo}>📁 {p.name}</div>{/each}
    </div>
    <button class="primary new" onclick={() => (selected = null)}>+ 새 요청</button>
    <div class="navs">
      <button class="nav" class:active={selected === 'projects'} onclick={() => (selected = 'projects')}>폴더 설정</button>
      <button class="nav" class:active={selected === 'employees'} onclick={() => (selected = 'employees')}>직원</button>
    </div>
    <div class="list">
      {#each requests as r (r.id)}
        <button class="item" class:active={selected === r.id} onclick={() => (selected = r.id)}>
          <div class="row1">
            <span class="status s-{r.status}">{running.includes(r.id) ? 'RUNNING' : r.status}</span>
            <span class="cost">{cost(r.costUsd)}</span>
          </div>
          <div class="text">{short(r.text)}</div>
          <div class="when">{modeName[r.mode] ?? ''}{r.project ? ' · ' + r.project : ''}</div>
          <div class="when">{r.id}</div>
        </button>
      {:else}
        <div class="empty">아직 요청이 없습니다</div>
      {/each}
    </div>
  </aside>

  <main>
    {#if error}<div class="err">{error}</div>{/if}
    {#if selected === null}
      <NewRequest onstarted={started} onemployees={() => (selected = 'employees')} onprojects={() => (selected = 'projects')} />
    {:else if selected === 'employees'}
      <Employees />
    {:else if selected === 'projects'}
      <Projects onchanged={refresh} />
    {:else}
      {#key selected}
        <RequestView id={selected} live={logs[selected] ?? []} isRunning={running.includes(selected)} {tick} onchanged={refresh} />
      {/key}
    {/if}
  </main>
</div>
{/if}

<style>
  .layout { display: grid; grid-template-columns: 300px 1fr; height: 100%; }
  aside { background: var(--panel); border-right: 1px solid var(--border); display: flex; flex-direction: column; min-height: 0; }
  .brand { display: flex; align-items: center; justify-content: space-between; gap: 6px; padding: 14px 12px 6px; }
  .wsname { background: transparent; border: none; padding: 2px 4px; font-weight: 700; font-size: 17px; color: var(--accent); text-align: left; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .wsbtns { display: flex; gap: 4px; }
  .small { padding: 3px 8px; font-size: 12px; }
  .folders { padding: 0 16px 10px; }
  .folder { font-size: 12px; color: var(--muted); padding: 2px 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .solo { height: 100%; }
  .new { margin: 0 12px 8px; }
  .navs { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; margin: 0 12px 12px; }
  .nav { background: transparent; }
  .nav.active { border-color: var(--accent); }
  .list { overflow-y: auto; flex: 1; padding: 0 8px 12px; }
  .item { display: block; width: 100%; text-align: left; background: transparent; border: 1px solid transparent; margin-bottom: 4px; padding: 9px 10px; }
  .item.active { background: var(--panel2); border-color: var(--border); }
  .row1 { display: flex; justify-content: space-between; align-items: center; }
  .cost { font-family: var(--mono); font-size: 12px; color: var(--muted); }
  .text { margin-top: 6px; line-height: 1.35; }
  .when { margin-top: 4px; font-family: var(--mono); font-size: 11px; color: var(--muted); }
  .empty { color: var(--muted); padding: 12px; }
  main { overflow-y: auto; padding: 24px 28px; min-width: 0; }
  .err { color: var(--red); margin-bottom: 12px; }
</style>
