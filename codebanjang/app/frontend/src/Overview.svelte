<script lang="ts">
  import { onMount } from 'svelte'
  import { Overview, Projects, Release } from '../wailsjs/go/main/App'
  import type { main, store, workspace } from '../wailsjs/go/models'

  let { tick, onopen }: { tick: number; onopen: (id: string) => void } = $props()

  let projects = $state<store.Project[]>([])
  let pid = $state(0)
  let ov = $state<main.Overview | null>(null)
  let error = $state('')
  let busy = $state(false)
  let note = $state('')

  async function load(fetch: boolean) {
    if (!pid) return
    busy = fetch
    try {
      const x = await Overview(pid, fetch)
      x.columns ??= []
      x.commits ??= []
      x.commitRequest ??= {}
      ov = x
      error = ''
    } catch (e) {
      error = String(e)
    } finally {
      busy = false
    }
  }

  onMount(async () => {
    projects = (await Projects()) ?? []
    pid = projects[0]?.id ?? 0
    await load(true)
  })

  // Requests finishing or being deployed change the board: reload without
  // going to GitHub.
  let lastTick = -1
  $effect(() => {
    if (tick !== lastTick) {
      const first = lastTick === -1
      lastTick = tick
      if (!first) load(false)
    }
  })

  async function release() {
    note = ''
    try {
      const r = await Release(pid)
      note = (r.created ? '운영 반영 요청(PR)을 만들었습니다' : '이미 열려 있는 운영 반영 요청이 있습니다') + ` — ${r.url}`
    } catch (e) {
      error = String(e)
    }
  }

  // ---- history graph (lanes like a git client) ----
  const H = 26 // row height
  const W = 14 // lane width
  const colors = ['#e8875b', '#58a6ff', '#3fb950', '#d2a8ff', '#f2cc60', '#ff7b72', '#79c0ff', '#56d4dd']
  type Seg = { x1: number; y1: number; x2: number; y2: number; c: string }
  type Row = { c: workspace.Commit; lane: number; color: string }

  let graph = $derived.by(() => {
    const rows: Row[] = []
    const segs: Seg[] = []
    const lanes: (string | null)[] = []
    const laneColor: number[] = []
    let next = 0
    const free = () => {
      const i = lanes.indexOf(null)
      return i >= 0 ? i : lanes.length
    }
    const X = (i: number) => i * W + W / 2
    for (const [r, c] of (ov?.commits ?? []).entries()) {
      const top = r * H, mid = top + H / 2, bot = top + H
      let lane = lanes.indexOf(c.hash)
      let fresh = false // a branch tip: no line comes from above
      if (lane < 0) {
        lane = free()
        lanes[lane] = c.hash
        laneColor[lane] = next++
        fresh = true
      }
      const color = colors[laneColor[lane] % colors.length]
      // Lines from above: lanes waiting for this commit join the node, the rest pass by.
      lanes.forEach((h, i) => {
        if (h === null || r === 0 || (fresh && i === lane)) return
        const lc = colors[laneColor[i] % colors.length]
        if (h === c.hash) segs.push({ x1: X(i), y1: top, x2: X(lane), y2: mid, c: lc })
        else segs.push({ x1: X(i), y1: top, x2: X(i), y2: mid, c: lc })
      })
      lanes.forEach((h, i) => { if (h === c.hash) lanes[i] = null })
      // Parents: the first continues this lane, others join or open lanes.
      const parents = c.parents ?? []
      const outs: number[] = []
      parents.forEach((p, k) => {
        let j = lanes.indexOf(p)
        if (j < 0) {
          j = k === 0 && lanes[lane] === null ? lane : free()
          lanes[j] = p
          laneColor[j] = k === 0 ? laneColor[lane] : next++
        }
        outs.push(j)
      })
      // Lines below: from the node to each parent lane, others continue down.
      lanes.forEach((h, i) => {
        if (h === null) return
        const lc = colors[laneColor[i] % colors.length]
        if (outs.includes(i)) segs.push({ x1: X(lane), y1: mid, x2: X(i), y2: bot, c: lc })
        else segs.push({ x1: X(i), y1: mid, x2: X(i), y2: bot, c: lc })
      })
      while (lanes.length && lanes[lanes.length - 1] === null) lanes.pop()
      rows.push({ c, lane, color })
    }
    const width = Math.max(1, ...rows.map((r) => r.lane + 1), ...segs.map((s) => Math.max(s.x1, s.x2) / W + 0.5)) * W
    return { rows, segs, width, height: rows.length * H }
  })

  // Requests by id, for the graph's links and branch labels.
  let byId = $derived.by(() => {
    const m: Record<string, main.BoardItem> = {}
    for (const col of ov?.columns ?? []) for (const r of col.items ?? []) m[r.id] = r
    return m
  })
  const isReqRef = (ref: string) => /^\d{8}-\d{6}-\d{3}$/.test(ref)
  const refClass = (ref: string) =>
    ref === ov?.prod ? 'prod' : ref === ov?.dev ? 'dev' : isReqRef(ref) ? 'req' : ref.includes('/') ? 'remote' : 'local'
  const refName = (ref: string) => (isReqRef(ref) ? '요청 ' + ref.slice(9, 15) : ref)
  const when = (s: string) => (s ? s.slice(5, 16).replace('T', ' ') : '')
  const short = (s: string, n = 46) => (s.length > n ? s.slice(0, n) + '…' : s)
  const ago = (s: string) => {
    const m = Math.round((Date.now() - Date.parse(s)) / 60000)
    return m < 60 ? `${m}분 전` : m < 1440 ? `${Math.round(m / 60)}시간 전` : `${Math.round(m / 1440)}일 전`
  }
</script>

<div class="top">
  <h1>작업 현황</h1>
  {#if projects.length > 1}
    <select bind:value={pid} onchange={() => load(true)}>
      {#each projects as p}<option value={p.id}>{p.name}</option>{/each}
    </select>
  {/if}
  <button disabled={busy} onclick={() => load(true)}>{busy ? 'GitHub 확인 중…' : 'GitHub에서 새로고침'}</button>
</div>
{#if error}<p class="err">{error}</p>{/if}
{#if ov?.fetchErr}<p class="warn">{ov.fetchErr}</p>{/if}

{#if ov}
  <div class="lines">
    {#if ov.remote}
      <div class="line">
        <span class="tag prod">운영</span><b class="mono">{ov.prod}</b>
        <span>마지막 반영 {when(ov.prodTime) || '—'}</span>
        <span class:hot={ov.devAhead > 0}>{ov.devAhead > 0 ? `개발에 운영 대기 ${ov.devAhead}건` : '개발과 같음'}</span>
        {#if ov.devAhead > 0}<button class="small" onclick={release}>운영 반영 요청</button>{/if}
      </div>
      <div class="line">
        <span class="tag dev">개발</span><b class="mono">{ov.dev}</b>
        <span>모든 반영은 여기로 · 운영보다 {ov.devAhead}건 앞섬</span>
      </div>
    {/if}
    <div class="line">
      <span class="tag pc">내 PC</span><b class="mono">{ov.localBranch || '(브랜치 아님)'}</b>
      <span class:hot={ov.localDirty > 0}>{ov.localDirty > 0 ? `커밋 안 한 변경 ${ov.localDirty}파일` : '커밋 안 한 변경 없음'}</span>
      {#if ov.remote}
        <span>{ov.dev}보다 {ov.localBehind}건 뒤{ov.localAhead ? ` · ${ov.localAhead}건 앞(안 올림)` : ''}</span>
        <span class="muted">AI 작업은 내 PC 상태와 상관없이 최신 {ov.dev}에서 시작합니다</span>
      {/if}
    </div>
  </div>
  {#if note}<p class="ok">{note}</p>{/if}

  <div class="board" style="grid-template-columns: repeat({ov.columns.length}, minmax(0, 1fr))">
    {#each ov.columns as col (col.key)}
      <div class="col">
        <div class="colhead"><b>{col.title}</b> <span class="count">{col.items.length}</span></div>
        <div class="hint">{col.hint}</div>
        {#each col.items as r (r.id)}
          <button class="card c-{col.key}" class:other={!r.local} disabled={!r.local} onclick={() => onopen(r.id)}
            title={r.local ? '요청 화면 열기' : '다른 PC에서 만든 요청입니다(GitHub 이력에서 읽음)'}>
            <div class="text">{short(r.text, 70)}</div>
            <div class="meta">
              <span class="who2" class:me={r.who === ov.me && r.local}>{r.who || '알 수 없음'}</span>
              · {col.key === 'running' ? ago(r.when) + ' 시작' : when(r.when)}{r.local ? ` · $${r.costUsd.toFixed(2)}` : ''}
            </div>
            {#if r.deployedBy && r.deployedBy !== r.who}<div class="meta">반영: {r.deployedBy}</div>{/if}
            {#if col.key === 'stopped' && r.reason}<div class="why">{short(r.reason, 60)}</div>{/if}
          </button>
        {:else}
          <div class="empty">없음</div>
        {/each}
      </div>
    {/each}
  </div>

  <h2>이력</h2>
  <div class="graph">
    <svg width={graph.width} height={graph.height} class="lanes">
      {#each graph.segs as s}
        {#if s.x1 === s.x2}
          <line x1={s.x1} y1={s.y1} x2={s.x2} y2={s.y2} stroke={s.c} stroke-width="2" />
        {:else}
          <path d="M{s.x1},{s.y1} C{s.x1},{(s.y1 + s.y2) / 2} {s.x2},{(s.y1 + s.y2) / 2} {s.x2},{s.y2}" stroke={s.c} stroke-width="2" fill="none" />
        {/if}
      {/each}
      {#each graph.rows as row, i}
        <circle cx={row.lane * W + W / 2} cy={i * H + H / 2} r="4.5" fill={row.color} stroke="var(--bg)" stroke-width="1.5" />
      {/each}
    </svg>
    <div class="rows" style="margin-left: {graph.width + 8}px">
      {#each graph.rows as row (row.c.hash)}
        {@const req = ov.commitRequest[row.c.hash]}
        <div class="row" style="height: {H}px">
          {#each row.c.refs as ref}<span class="ref r-{refClass(ref)}">{refName(ref)}</span>{/each}
          {#if req && byId[req] && !byId[req].local}
            <span class="subj" title="다른 PC의 요청">{short(row.c.subject.replace(/^(코드반장|AI Office): /, '🤖 '), 50)} <span class="who2">{byId[req].who}</span></span>
          {:else if req}
            <button class="link" onclick={() => onopen(req)} title="요청 화면 열기">{short(row.c.subject.replace(/^(코드반장|AI Office): /, '🤖 '), 60)}</button>
          {:else}
            <span class="subj">{short(row.c.subject, 60)}</span>
          {/if}
          <span class="who">{row.c.author}</span>
          <span class="date">{when(row.c.date)}</span>
          <span class="hash">{row.c.hash.slice(0, 7)}</span>
        </div>
      {/each}
    </div>
  </div>
{/if}

<style>
  .top { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; }
  h1 { margin: 0; font-size: 22px; flex: 1; }
  h2 { font-size: 16px; margin: 22px 0 8px; }
  .lines { display: grid; gap: 6px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 12px 14px; }
  .line { display: flex; align-items: center; gap: 14px; font-size: 13px; color: var(--muted); flex-wrap: wrap; }
  .line b { color: var(--text); }
  .tag { font-size: 11px; padding: 2px 8px; border-radius: 10px; color: #0d1117; font-weight: 700; min-width: 44px; text-align: center; }
  .tag.prod { background: #ff7b72; }
  .tag.dev { background: #58a6ff; }
  .tag.pc { background: #8b949e; }
  .hot { color: var(--accent); }
  .small { padding: 3px 10px; font-size: 12px; }
  .mono { font-family: var(--mono); }
  .muted { color: var(--muted); font-size: 12px; }
  .board { display: grid; gap: 10px; margin-top: 14px; }
  .col { background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 10px; min-height: 120px; }
  .colhead { display: flex; justify-content: space-between; }
  .count { font-family: var(--mono); color: var(--muted); }
  .hint { color: var(--muted); font-size: 11px; margin: 2px 0 8px; }
  .card { display: block; width: 100%; text-align: left; margin-bottom: 6px; padding: 8px 9px; background: var(--panel2); border-left: 3px solid var(--border); }
  .c-running { border-left-color: var(--accent); }
  .c-done { border-left-color: var(--green); }
  .c-dev { border-left-color: #58a6ff; }
  .c-prod, .c-merged { border-left-color: #ff7b72; }
  .c-stopped { border-left-color: var(--muted); opacity: .8; }
  .card.other { cursor: default; opacity: 1; }
  .card:disabled { opacity: 1; }
  .who2 { color: var(--text); font-family: inherit; }
  .who2.me { color: var(--accent); }
  .text { line-height: 1.35; font-size: 13px; }
  .meta { color: var(--muted); font-size: 11px; margin-top: 4px; font-family: var(--mono); }
  .why { color: var(--red); font-size: 11px; margin-top: 3px; }
  .empty { color: var(--muted); font-size: 12px; }
  .graph { position: relative; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 8px 10px; overflow-x: auto; }
  .lanes { position: absolute; left: 10px; top: 8px; }
  .row { display: flex; align-items: center; gap: 8px; font-size: 13px; white-space: nowrap; }
  .subj { overflow: hidden; text-overflow: ellipsis; }
  .link { background: none; border: none; padding: 0; color: var(--text); text-decoration: underline dotted; cursor: pointer; font-size: 13px; }
  .who, .date, .hash { color: var(--muted); font-size: 11px; font-family: var(--mono); }
  .who { margin-left: auto; }
  .ref { font-size: 11px; padding: 1px 7px; border-radius: 9px; font-family: var(--mono); border: 1px solid var(--border); }
  .r-prod { background: #ff7b72; color: #0d1117; border-color: #ff7b72; }
  .r-dev { background: #58a6ff; color: #0d1117; border-color: #58a6ff; }
  .r-req { color: var(--green); border-color: var(--green); }
  .r-local { color: var(--text); }
  .r-remote { color: var(--muted); }
  .err { color: var(--red); }
  .warn { color: var(--accent); font-size: 13px; }
  .ok { color: var(--green); font-size: 13px; word-break: break-all; }
</style>
