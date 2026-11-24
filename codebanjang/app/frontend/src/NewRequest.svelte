<script lang="ts">
  import { onMount } from 'svelte'
  import { Employees, LastProjectID, Projects, Start } from '../wailsjs/go/main/App'
  import type { main, store } from '../wailsjs/go/models'

  let { onstarted, onemployees, onprojects }: {
    onstarted: (id: string) => void; onemployees: () => void; onprojects: () => void
  } = $props()

  let projects = $state<store.Project[]>([])
  let projectId = $state(0)
  let staff = $state<main.EmployeeView[]>([])
  let team = $state<Record<string, number>>({ ask: 0, pm: 0, dev: 0, qa: 0 })
  let request = $state('')
  let mode = $state('review')
  let budget = $state(2)
  // Meetings: stage -> employee ids in speaking order (first = author / moderator).
  let panels = $state<Record<string, number[]>>({ plan: [], review: [], meeting: [] })
  let useMeeting = $state<Record<string, boolean>>({ plan: false, review: false, meeting: true })
  let rounds = $state(2)
  const stageInfo: Record<string, { title: string; how: string; first: string }> = {
    plan: { title: '기획 회의', how: '첫 번째 사람이 초안을 쓰고, 나머지가 비판하면 고칩니다. 모두 승인하거나 라운드가 끝나면 종료', first: '초안 작성' },
    review: { title: 'QA 교차 검토', how: '각자 따로 검토한 뒤 서로의 지적을 확인해서, 과반이 맞다고 한 지적만 남깁니다', first: '' },
    meeting: { title: '회의', how: '각자 입장을 낸 뒤 서로 반박하고, 첫 번째 사람이 사회자로 결론을 정리합니다', first: '사회' },
  }
  const stagesFor: Record<string, string[]> = { plan: ['plan'], audit: ['review'], team: ['plan'], review: ['plan', 'review'], meeting: ['meeting'] }
  let stages = $derived(stagesFor[mode] ?? [])
  function toggleMember(stage: string, id: number) {
    const cur = panels[stage]
    panels[stage] = cur.includes(id) ? cur.filter((x) => x !== id) : cur.length >= 4 ? cur : [...cur, id]
  }
  let fixRounds = $state(3)
  let busy = $state(false)
  let error = $state('')

  type Mode = { v: string; name: string; desc: string; slots: string[] }
  const readModes: Mode[] = [
    { v: 'ask', name: '질문하기', desc: '프로젝트 코드를 읽고 질문에 답합니다', slots: ['ask'] },
    { v: 'plan', name: '기획만', desc: 'PM이 현재 구조에 맞춘 기획서를 씁니다', slots: ['pm'] },
    { v: 'audit', name: '검토만', desc: 'QA가 지금 코드의 결함을 찾아 보고합니다', slots: ['qa'] },
    { v: 'meeting', name: '회의', desc: '여러 직원이 입장을 내고 반박한 뒤 사회자가 결론을 냅니다', slots: [] },
  ]
  const codeModes: Mode[] = [
    { v: 'solo', name: '혼자 개발', desc: '개발자 한 명이 요청 전체를 수행', slots: ['dev'] },
    { v: 'team', name: '팀', desc: 'PM이 작업을 나누고 개발자가 수행, 검사로 확인', slots: ['pm', 'dev'] },
    { v: 'review', name: '팀 + QA 리뷰', desc: '검사 통과 뒤 QA가 변경 내용을 요청과 비교', slots: ['pm', 'dev', 'qa'] },
  ]
  const allModes = [...readModes, ...codeModes]
  const slotName: Record<string, string> = { ask: '답변할 직원', pm: 'PM', dev: '개발자', qa: 'QA' }
  let current = $derived(allModes.find((m) => m.v === mode)!)
  let isCode = $derived(codeModes.some((m) => m.v === mode))
  let project = $derived(projects.find((p) => p.id === projectId))

  // "ask" can go to anyone; other slots only to employees with that role.
  const candidates = (slot: string) => (slot === 'ask' ? staff : staff.filter((e) => e.role === slot))

  onMount(async () => {
    try {
      ;[projects, staff] = await Promise.all([Projects().then((x) => x ?? []), Employees().then((x) => x ?? [])])
      const last = await LastProjectID()
      projectId = projects.some((p) => p.id === last) ? last : (projects[0]?.id ?? 0)
      for (const slot of ['ask', 'pm', 'dev', 'qa']) team[slot] = candidates(slot)[0]?.id ?? 0
    } catch (e) {
      error = String(e)
    }
  })

  async function start() {
    error = ''
    busy = true
    try {
      const chosen: Record<string, number[]> = {}
      for (const st of stages) if (useMeeting[st] || st === 'meeting') chosen[st] = panels[st]
      const id = await Start({ projectId, request, mode, team, panels: chosen, rounds: Number(rounds), budgetUsd: Number(budget), fixRounds: Number(fixRounds) } as any)
      request = ''
      onstarted(id)
    } catch (e) {
      error = String(e)
    } finally {
      busy = false
    }
  }

  const account = (e: main.EmployeeView) =>
    e.connection === 'ollama' ? (e.status?.loggedIn ? 'Ollama' : 'Ollama · 사용 불가')
      : e.connection === 'own' ? (e.status?.loggedIn ? '전용 계정' : '전용 계정 · 로그인 필요') : '기본 로그인'
</script>

<h1>새 요청</h1>

<span class="lbl">프로젝트 <button class="link" onclick={onprojects}>폴더 설정</button></span>
{#if projects.length === 0}
  <div class="empty">이 작업공간에 폴더가 없습니다. <button class="primary" onclick={onprojects}>폴더 추가</button></div>
{:else}
  <select bind:value={projectId}>
    {#each projects as p}<option value={p.id}>{p.name} — {p.repo}</option>{/each}
  </select>
  {#if project}
    <p class="small">검사: {project.checks?.length ? project.checks.join(' · ') : '없음'} · 보호: {project.protect?.length ? project.protect.join(', ') : '없음'}{project.notes ? ' · 프로젝트 설명 있음' : ''}</p>
  {/if}
{/if}

<label for="req">요청</label>
<textarea id="req" rows="6" bind:value={request} placeholder={mode === 'ask' ? '무엇이 궁금한가요? 예: 결제 처리 흐름이 어떻게 되어 있어?' : mode === 'plan' ? '무엇을 기획할까요? 예: 월별 통계 화면을 추가하고 싶어' : mode === 'audit' ? '어디를 검토할까요? (비우면 핵심 부분 전체)' : '무엇을 해 달라고 할까요?'}></textarea>

<span class="lbl">진행 방식</span>
<div class="group">읽기만 · 프로젝트 파일을 바꾸지 않음</div>
<div class="modes">
  {#each readModes as m}
    <button class="mode" class:on={mode === m.v} onclick={() => (mode = m.v)}><b>{m.name}</b><span>{m.desc}</span></button>
  {/each}
</div>
<div class="group">코드 수정 · 별도 브랜치에서 작업, 승인해야 반영</div>
<div class="modes">
  {#each codeModes as m}
    <button class="mode" class:on={mode === m.v} onclick={() => (mode = m.v)}><b>{m.name}</b><span>{m.desc}</span></button>
  {/each}
</div>

{#if current.slots.length}<span class="lbl">참여 직원 <button class="link" onclick={onemployees}>직원 관리</button></span>{/if}
<div class="slots">
  {#each current.slots as slot (slot)}
    <div>
      <label for={'t-' + slot}>{slotName[slot]}</label>
      <select id={'t-' + slot} bind:value={team[slot]}>
        {#each candidates(slot) as e}
          <option value={e.id}>{e.name} · {e.model} · {account(e)}</option>
        {:else}
          <option value={0}>이 역할의 직원이 없습니다</option>
        {/each}
      </select>
    </div>
  {/each}
</div>

{#each stages as st (st)}
  <div class="meeting">
    {#if st !== 'meeting'}
      <label class="check"><input type="checkbox" bind:checked={useMeeting[st]} /> <b>{stageInfo[st].title}</b>로 진행</label>
    {:else}
      <b>{stageInfo[st].title} 참석자</b>
    {/if}
    <p class="small">{stageInfo[st].how}</p>
    {#if st === 'meeting' || useMeeting[st]}
      <div class="chips">
        {#each staff as e}
          {@const n = panels[st].indexOf(e.id)}
          <button class="chip" class:on={n >= 0} onclick={() => toggleMember(st, e.id)}>
            {#if n >= 0}<span class="num">{n + 1}</span>{/if}{e.name} · {e.model}{n === 0 && stageInfo[st].first ? ' · ' + stageInfo[st].first : ''}
          </button>
        {/each}
      </div>
      <p class="small">{panels[st].length}명 선택 (2~4명, 누른 순서대로)</p>
    {/if}
  </div>
{/each}

<div class="slots">
  {#if stages.some((st) => (st === 'meeting' || useMeeting[st]) && st !== 'review')}
    <div>
      <label for="rounds">회의 라운드</label>
      <input id="rounds" type="number" min="1" max="4" bind:value={rounds} />
    </div>
  {/if}
  <div>
    <label for="budget">예산 (요청 전체, USD)</label>
    <input id="budget" type="number" min="0.1" step="0.5" bind:value={budget} />
  </div>
  {#if isCode}
    <div>
      <label for="fix">최대 수정 횟수</label>
      <input id="fix" type="number" min="0" max="10" bind:value={fixRounds} />
    </div>
  {/if}
</div>

{#if error}<p class="err">{error}</p>{/if}
<div class="actions">
  <button class="primary" disabled={busy || !projectId || !request.trim()} onclick={start}>시작</button>
</div>

<style>
  h1 { margin: 0 0 6px; font-size: 22px; }
  .lbl { display: block; color: var(--muted); font-size: 12px; margin: 16px 0 6px; }
  .group { color: var(--muted); font-size: 12px; margin: 10px 0 6px; }
  .small { color: var(--muted); font-size: 12px; margin: 6px 0 0; }
  .modes { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 10px; }
  .meeting { margin-top: 14px; background: var(--panel); border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; }
  .check { display: flex; align-items: center; gap: 8px; color: var(--text); font-size: 14px; margin: 0; }
  .check input { width: auto; }
  .chips { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
  .chip { font-size: 12px; padding: 5px 10px; border-radius: 14px; }
  .chip.on { border-color: var(--accent); background: rgba(232, 135, 95, .12); }
  .num { display: inline-block; background: var(--accent); color: #111; border-radius: 8px; padding: 0 6px; margin-right: 6px; font-weight: 700; }
  .mode { text-align: left; padding: 10px 12px; display: flex; flex-direction: column; gap: 4px; }
  .mode span { color: var(--muted); font-size: 12px; line-height: 1.4; }
  .mode.on { border-color: var(--accent); background: rgba(232, 135, 95, .1); }
  .slots { display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; }
  .empty { display: flex; gap: 12px; align-items: center; color: var(--muted); }
  .actions { margin-top: 20px; }
  .err { color: var(--red); }
  .link { background: none; border: none; color: var(--blue); padding: 0 0 0 8px; font-size: 12px; }
</style>
