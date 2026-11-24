<script lang="ts">
  import { onMount } from 'svelte'
  import { DecideRule, Employees, Knowledge, RuleCandidates, SaveKnowledge, Start } from '../wailsjs/go/main/App'
  import type { main, store } from '../wailsjs/go/models'

  let { project, draft, onclose, onstarted }: {
    project: store.Project; draft: string | null; onclose: () => void; onstarted: (id: string) => void
  } = $props()

  let view = $state<main.KnowledgeView | null>(null)
  let text = $state('')
  let fromDraft = $state(false)
  let staff = $state<main.EmployeeView[]>([])
  let who = $state(0)
  let focus = $state('')
  let budget = $state(3)
  let error = $state('')
  let saved = $state('')
  let busy = $state(false)

  // Rules proposed from past requests, waiting for a person.
  let cands = $state<any[]>([])
  async function loadCands() {
    cands = ((await RuleCandidates(project.id)) ?? []).map((c: any) => ({ ...c, edit: c.rule }))
  }
  async function decide(c: any, accept: boolean) {
    if (accept && dirty) { error = '문서에 저장하지 않은 수정이 있습니다. 먼저 저장하세요.'; return }
    error = ''
    try {
      await DecideRule(c.id, accept, c.edit)
      if (accept) { view = await Knowledge(project.id); text = view.text; fromDraft = false }
      await loadCands()
    } catch (e) {
      error = String(e)
    }
  }

  onMount(async () => {
    loadCands()
    try {
      view = await Knowledge(project.id)
      text = draft ?? view.text
      fromDraft = draft !== null
      staff = (await Employees()) ?? []
      // Prefer someone on a cloud model: reading a whole project needs a strong one.
      who = (staff.find((e) => e.connection !== 'ollama' && e.role === 'pm') ?? staff.find((e) => e.connection !== 'ollama') ?? staff[0])?.id ?? 0
    } catch (e) {
      error = String(e)
    }
  })

  async function save() {
    error = ''
    busy = true
    try {
      view = await SaveKnowledge(project.id, text)
      fromDraft = false
      saved = '저장했습니다.'
    } catch (e) {
      error = String(e)
    } finally {
      busy = false
    }
  }

  async function learn() {
    if (text !== (view?.text ?? '') && !confirm('저장하지 않은 수정이 있습니다. 버리고 AI에게 다시 파악시킬까요?')) return
    error = ''
    busy = true
    try {
      const id = await Start({
        projectId: project.id, mode: 'learn', team: { ask: who }, panels: {}, rounds: 0, fixRounds: 0,
        budgetUsd: Number(budget), request: focus.trim() || '프로젝트 전체를 파악해서 지식 문서를 써 줘.',
      } as any)
      onstarted(id)
    } catch (e) {
      error = String(e)
    } finally {
      busy = false
    }
  }

  const gitNote: Record<string, string> = {
    new: '아직 git에 없음 — 커밋하고 push해야 다른 사람에게 전달됩니다',
    changed: '마지막 커밋 뒤에 바뀜 — 커밋하고 push해야 다른 사람에게 전달됩니다',
    '': '커밋됨',
  }
  let dirty = $derived(text !== (view?.text ?? ''))
</script>

<div class="card edit">
  <div class="head">
    <div>
      <b>{project.name} — 프로젝트 지식 문서</b>
      <div class="mono muted">{project.repo}\{view?.path.replaceAll('/', '\\')}</div>
    </div>
    <button onclick={() => { if (!dirty || confirm('저장하지 않은 수정을 버릴까요?')) onclose() }}>닫기</button>
  </div>
  <p class="hint">
    모든 직원이 일을 시작하기 전에 읽는 문서입니다(구조, 실행 방법, 화면·기능 → 코드 지도, 용어, 위험 영역).
    저장소 안에 저장되므로 git으로 함께 넘어가고, 사람에게는 인수인계 문서가 됩니다.
    <b>사람이 적은 내용</b> 칸은 AI가 다시 써도 그대로 남습니다. AI는 이 문서를 고칠 수 없고, 여기서 검토한 내용만 저장됩니다.
  </p>

  {#if cands.length}
    <div class="cands">
      <b>작업 이력에서 찾은 규칙 후보 {cands.length}개</b>
      <span class="muted small">사람이 끼어들었던 요청(추가 지시·검사 실패·리뷰 지적)에서 AI가 뽑은 것입니다. 추가하면 문서의 "작업 이력에서 배운 규칙"에 들어가고, 다음 요청부터 모든 직원이 읽습니다.</span>
      {#each cands as c (c.id)}
        <div class="cand">
          <input bind:value={c.edit} />
          <div class="why">{c.why} <span class="mono">요청 {c.requestId}</span></div>
          <div class="cbtn">
            <button class="primary" onclick={() => decide(c, true)}>문서에 추가</button>
            <button onclick={() => decide(c, false)}>버리기</button>
          </div>
        </div>
      {/each}
    </div>
  {/if}
  {#if fromDraft}<div class="banner">AI가 쓴 초안입니다. 틀린 곳을 고치고 "사람이 적은 내용"을 채운 뒤 저장하세요. 저장 전에는 아무것도 바뀌지 않습니다.</div>{/if}
  {#if view}
    <div class="kstatus">
      {#if view.exists}
        <span>{view.chars.toLocaleString()}자</span>
        <span class:warn={view.git !== ''}>{gitNote[view.git] ?? view.git}</span>
        {#if view.capped}<span class="warn">3만 자를 넘어 지시에는 앞부분만 들어갑니다</span>{/if}
      {:else}
        <span class="warn">아직 지식 문서가 없습니다. 아래에서 AI에게 초안을 맡기거나 직접 쓰세요.</span>
      {/if}
    </div>
  {/if}

  <textarea rows="22" bind:value={text} spellcheck="false" placeholder="# 프로젝트 지식 문서"></textarea>
  <div class="actions">
    <button class="primary" disabled={busy || !dirty && !fromDraft || !text.trim()} onclick={save}>저장</button>
    {#if saved && !dirty}<span class="muted">{saved}</span>{/if}
  </div>
  {#if error}<p class="err">{error}</p>{/if}

  <div class="learn">
    <b>AI에게 파악시키기</b>
    <p class="hint">직원이 저장소를 읽고 {view?.exists ? '지금 문서를 코드에 맞게 고친' : '새'} 초안을 씁니다. 초안은 요청 화면에 나오고, 검토해서 저장하기 전에는 문서가 바뀌지 않습니다. 프로젝트 크기에 따라 몇 분, 수십 센트~몇 달러가 듭니다.</p>
    <div class="row">
      <div>
        <label for="kw">직원</label>
        <select id="kw" bind:value={who}>
          {#each staff as e}<option value={e.id}>{e.name} · {e.model}{e.connection === 'ollama' ? ' · Ollama(약함)' : ''}</option>{/each}
        </select>
      </div>
      <div class="grow">
        <label for="kf">특히 볼 부분 (비우면 전체)</label>
        <input id="kf" bind:value={focus} placeholder="예: 주문·결제 흐름과 관리자 화면" />
      </div>
      <div>
        <label for="kb">예산($)</label>
        <input id="kb" type="number" min="0.1" step="0.5" bind:value={budget} class="num" />
      </div>
      <button disabled={busy || !who} onclick={learn}>{view?.exists ? '다시 파악' : '파악 시작'}</button>
    </div>
  </div>
</div>

<style>
  .card { background: var(--panel); border: 1px solid var(--accent); border-radius: 10px; padding: 14px 16px; margin-bottom: 16px; }
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: 12px; }
  .mono { font-family: var(--mono); font-size: 12px; margin-top: 4px; }
  .muted { color: var(--muted); }
  .hint { color: var(--muted); line-height: 1.5; margin: 10px 0; font-size: 13px; }
  .banner { background: var(--panel2); border-left: 3px solid var(--accent); padding: 8px 12px; margin-bottom: 10px; }
  .kstatus { display: flex; gap: 18px; font-size: 12px; color: var(--muted); margin-bottom: 8px; }
  .warn { color: var(--accent); }
  textarea { width: 100%; font-family: var(--mono); font-size: 13px; line-height: 1.5; }
  .actions { display: flex; gap: 10px; align-items: center; margin-top: 10px; }
  .cands { border: 2px solid var(--accent); border-radius: 10px; padding: 12px 14px; margin-bottom: 12px; display: grid; gap: 8px; }
  .cand { background: var(--panel2); border-radius: 8px; padding: 10px; display: grid; gap: 6px; }
  .why { color: var(--muted); font-size: 12px; }
  .cbtn { display: flex; gap: 8px; }
  .small { font-size: 12px; }
  .learn { border-top: 1px solid var(--border); margin-top: 16px; padding-top: 12px; }
  .row { display: flex; gap: 12px; align-items: flex-end; }
  .grow { flex: 1; }
  .num { width: 80px; }
  .err { color: var(--red); }
</style>
