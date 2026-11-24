<script lang="ts">
  import { Approve, Detail, Discard, Stop } from '../wailsjs/go/main/App'
  import type { main } from '../wailsjs/go/models'

  type Log = { id: string; t: number; role: string; line: string }
  let { id, live, isRunning, tick, onchanged, onknowledge }: {
    id: string; live: Log[]; isRunning: boolean; tick: number; onchanged: () => void
    onknowledge: (projectId: number, draft: string) => void
  } = $props()

  let d = $state<main.Detail | null>(null)
  let error = $state('')
  let busy = $state(false)
  let tab = $state<'answer' | 'meeting' | 'log' | 'diff' | 'report'>('log')
  let readOnly = $derived(['ask', 'plan', 'audit', 'meeting', 'learn'].includes(d?.request.mode ?? ''))
  let shownAnswer = false
  let shownMeeting = false
  // Who is talking right now: speakers whose latest log line is not "done:".
  let speaking = $derived.by(() => {
    const last: Record<string, string> = {}
    for (const l of live) if (!['system', 'meeting', 'pm', 'dev', 'qa', 'ask'].includes(l.role)) last[l.role] = l.line
    return Object.entries(last).filter(([, line]) => !line.startsWith('done:')).map(([name]) => name)
  })

  async function load() {
    try {
      const x = await Detail(id)
      x.tasks ??= []
      x.checks ??= []
      x.messages ??= []
      d = x
      if (isRunning && x.messages.length && !shownMeeting) {
        tab = 'meeting' // watch the meeting while it happens
        shownMeeting = true
      }
      if (x.answer && !shownAnswer) {
        tab = 'answer' // jump to the answer once it arrives
        shownAnswer = true
      }
      error = ''
    } catch (e) {
      error = String(e)
    }
  }

  // Reload when the request finishes, and while it runs whenever a log line arrives.
  $effect(() => {
    tick
    live.length
    load()
  })

  async function act(fn: (id: string) => Promise<void>, confirmText: string) {
    if (confirmText && !confirm(confirmText)) return
    busy = true
    try {
      await fn(id)
      await load()
      onchanged()
    } catch (e) {
      error = String(e)
    } finally {
      busy = false
    }
  }

  const roleColor: Record<string, string> = { pm: 'var(--blue)', dev: 'var(--accent)', qa: 'var(--green)', ask: 'var(--blue)', system: 'var(--muted)' }
  const kindName: Record<string, string> = { plan: '계획', work: '작업', fix: '수정', review: 'QA 리뷰', answer: '답변', setup: '준비' }
  const kindLabel: Record<string, string> = {
    finding: '검토', verdict: '교차 확인', conclusion: '결론', draft: '초안', critique: '비판', revision: '수정본',
    opinion: '입장', rebuttal: '반박',
  }
  // A few readable lines from a message's structured data.
  function detailLines(m: { kind: string; data: string }): string[] {
    let x: any
    try { x = JSON.parse(m.data) } catch { return [] }
    switch (m.kind) {
      case 'finding': return (x.issues ?? []).map((i: any) => `• [${i.severity}] ${i.file}: ${i.problem}`)
      case 'verdict': return (x.verdicts ?? []).map((v: any) => `• ${v.id} → ${v.verdict === 'valid' ? '맞음' : v.verdict === 'invalid' ? '틀림' : '모름'}: ${v.reason}${v.evidence ? ' [근거: ' + v.evidence + ']' : ''}`)
      case 'conclusion': return Array.isArray(x) ? x.map((f: any) => `• ${f.accepted ? '채택' : '기각'} ${f.id} (${f.author}) ${f.problem}`) : []
      case 'critique': return (x.points ?? []).map((p: any) => `• ${p.point} — ${p.suggestion}`)
      case 'opinion': case 'rebuttal':
        return [...(x.reasons ?? []).map((r: string) => '• ' + r), ...(x.agree_with?.length ? ['동의: ' + x.agree_with.join(', ')] : []),
          ...(x.disagree ?? []).map((g: any) => `반대(${g.name}): ${g.why}`), ...(x.changed ? ['(입장을 바꿈)'] : [])]
    }
    return []
  }
  const diffClass = (l: string) =>
    l.startsWith('+++') || l.startsWith('---') ? 'dh' : l.startsWith('+') ? 'da' : l.startsWith('-') ? 'dd' : l.startsWith('@@') ? 'dh' : ''
</script>

{#if d}
  {@const r = d.request}
  <div class="head">
    <div>
      <span class="status s-{isRunning ? 'RUNNING' : r.status}">{isRunning ? 'RUNNING' : r.status}</span>
      <span class="mono muted">{r.id}{r.branch ? ' · ' + r.branch : ''}{r.project ? ' · ' + r.project : ''}</span>
    </div>
    <div class="buttons">
      {#if isRunning}
        <button class="danger" disabled={busy} onclick={() => act(async (x) => Stop(x), '실행을 중지할까요? 실행 중인 AI 프로세스가 모두 종료됩니다.')}>중지</button>
      {:else if r.mode === 'learn'}
        <button class="ok" disabled={!d.answer || r.status !== 'DONE'} onclick={() => onknowledge(r.projectId, d?.answer ?? '')}>검토하고 지식 문서로 저장</button>
      {:else if readOnly}
        <button onclick={() => navigator.clipboard.writeText(d?.answer ?? '')} disabled={!d.answer}>답변 복사</button>
      {:else if r.status === 'DONE'}
        {#if d.deployTarget}
          <button class="ok" disabled={busy} onclick={() => act(Approve, `이 변경을 개발 브랜치(${d?.deployTarget})에 반영할까요?\n\n최신 ${d?.deployTarget}와 합친 뒤 준비 명령과 검사를 다시 돌리고, 통과하면 GitHub에 push합니다. 다른 사람의 변경과 충돌하거나 검사가 실패하면 반영하지 않습니다.`)}>{busy ? '반영 중… (검사 다시 실행)' : `개발(${d.deployTarget})에 반영`}</button>
        {:else}
          <button class="ok" disabled={busy} onclick={() => act(Approve, `이 변경을 원래 저장소에 반영(merge)할까요?\n${r.repo}`)}>승인하고 반영</button>
        {/if}
        <button disabled={busy} onclick={() => act(Discard, '작업 폴더와 브랜치를 지울까요?')}>폐기</button>
      {:else if r.status === 'FAILED' || r.status === 'CANCELLED' || r.status === 'INTERRUPTED'}
        <button disabled={busy} onclick={() => act(Discard, '작업 폴더와 브랜치를 지울까요?')}>폐기</button>
      {/if}
    </div>
  </div>
  <h2>{r.text}</h2>
  {#if r.reason}<p class="reason" class:good={r.status === 'DEV'}>{r.status === 'DEV' ? '개발 반영됨: ' : ''}{r.reason}</p>{/if}
  {#if r.plan}<p class="plan"><b>PM 계획</b> {r.plan}</p>{/if}

  <div class="stats">
    <div><span>비용</span><b>${r.costUsd.toFixed(4)}</b></div>
    <div><span>AI 실행</span><b>{r.runs}회</b></div>
    {#if !readOnly}<div><span>검사</span><b>{d.checks.filter((c) => c.exitCode === 0 && !c.timedOut).length}/{d.checks.length} 통과</b></div>
    <div><span>수정</span><b>{d.tasks.filter((t) => t.kind === 'fix').length}회</b></div>{/if}
  </div>

  <table>
    <thead><tr><th>#</th><th>단계</th><th>담당</th><th>내용</th><th>상태</th><th>비용</th><th>시간</th></tr></thead>
    <tbody>
      {#each d.tasks as t (t.id)}
        <tr>
          <td class="mono">{t.seq}</td>
          <td>{kindName[t.kind] ?? t.kind}</td>
          <td style="color:{roleColor[t.role]}">{t.role.toUpperCase()}</td>
          <td>{t.title}</td>
          <td><span class="status s-{t.status === 'COMPLETED' ? 'DONE' : t.status === 'IN_PROGRESS' ? 'RUNNING' : t.status}">{t.status}</span></td>
          <td class="mono">${t.costUsd.toFixed(4)}{t.estimated ? '*' : ''}</td>
          <td class="mono">{t.seconds.toFixed(0)}s</td>
        </tr>
      {/each}
    </tbody>
  </table>

  <div class="tabs">
    {#if readOnly}<button class:on={tab === 'answer'} onclick={() => (tab = 'answer')}>{r.mode === 'plan' ? '기획서' : r.mode === 'audit' ? '검토 결과' : r.mode === 'learn' ? '지식 문서 초안' : '답변'}</button>{/if}
    {#if d.messages.length}<button class:on={tab === 'meeting'} onclick={() => (tab = 'meeting')}>회의 ({d.messages.length})</button>{/if}
    <button class:on={tab === 'log'} onclick={() => (tab = 'log')}>진행 기록</button>
    {#if !readOnly}<button class:on={tab === 'diff'} onclick={() => (tab = 'diff')}>변경 내용</button>{/if}
    <button class:on={tab === 'report'} onclick={() => (tab = 'report')}>보고서</button>
  </div>
  <div class="box">
    {#if tab === 'answer'}
      {#if d.answer}<div class="answer">{d.answer}</div>{:else}<p class="muted">아직 답변이 없습니다.</p>{/if}
    {:else if tab === 'meeting'}
      {#if isRunning && speaking.length}
        <div class="speaking">지금 발언 중: {#each speaking as n}<span class="who">{n}</span>{/each}<span class="dots">…</span></div>
      {/if}
      {#each d.messages as m (m.id)}
        <div class="msg" class:sys={m.sender === 'AI Office'}>
          <div class="mh"><b>{m.sender}</b> <span class="kind k-{m.kind}">{kindLabel[m.kind] ?? m.kind}</span> <span class="muted">{m.round}라운드{m.costUsd ? ' · $' + m.costUsd.toFixed(3) : ''}</span></div>
          <div class="mc">{m.content}</div>
          {#each detailLines(m) as line}<div class="ml">{line}</div>{/each}
        </div>
      {/each}
    {:else if tab === 'log'}
      {#if live.length === 0}
        <p class="muted">실시간 기록은 이 창에서 시작한 요청에만 보입니다. 지난 요청은 표와 보고서를 보세요.</p>
      {/if}
      {#each live as l}
        <div class="log"><span class="t">{l.t.toFixed(1)}s</span><span class="role" style="color:{roleColor[l.role]}">{l.role}</span><span>{l.line}</span></div>
      {/each}
    {:else if tab === 'diff'}
      {#if d.diff}
        <pre>{#each d.diff.split('\n') as l}<span class={diffClass(l)}>{l}{'\n'}</span>{/each}</pre>
      {:else}<p class="muted">변경 내용이 없거나 작업 폴더가 지워졌습니다.</p>{/if}
    {:else}
      <pre>{d.report}</pre>
    {/if}
  </div>
{/if}
{#if error}<p class="err">{error}</p>{/if}

<style>
  .head { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
  .buttons { display: flex; gap: 8px; }
  .mono { font-family: var(--mono); }
  .muted { color: var(--muted); }
  .head .mono { margin-left: 8px; font-size: 12px; }
  h2 { font-size: 18px; margin: 14px 0 6px; line-height: 1.4; }
  .reason { color: var(--red); margin: 4px 0; }
  .reason.good { color: var(--green); font-family: var(--mono); font-size: 13px; }
  .plan { color: var(--muted); margin: 6px 0; line-height: 1.5; }
  .plan b { color: var(--blue); margin-right: 6px; }
  .stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; margin: 16px 0; }
  .stats div { background: var(--panel); border: 1px solid var(--border); border-radius: 8px; padding: 10px 14px; }
  .stats span { display: block; color: var(--muted); font-size: 12px; }
  .stats b { font-size: 18px; font-family: var(--mono); }
  table { width: 100%; border-collapse: collapse; margin-bottom: 18px; }
  th { text-align: left; color: var(--muted); font-weight: 400; font-size: 12px; border-bottom: 1px solid var(--border); padding: 6px 8px; }
  td { padding: 7px 8px; border-bottom: 1px solid #21262d; vertical-align: top; }
  .tabs { display: flex; gap: 6px; margin-bottom: -1px; }
  .tabs button { border-radius: 6px 6px 0 0; background: transparent; }
  .tabs button.on { background: var(--panel); border-bottom-color: var(--panel); }
  .box { background: var(--panel); border: 1px solid var(--border); border-radius: 0 8px 8px 8px; padding: 14px; min-height: 240px; max-height: 520px; overflow: auto; }
  .log { font-family: var(--mono); font-size: 12.5px; line-height: 1.6; display: flex; gap: 10px; }
  .log .t { color: var(--muted); width: 56px; text-align: right; flex: none; }
  .log .role { width: 48px; flex: none; }
  .da { color: var(--green); }
  .dd { color: var(--red); }
  .dh { color: var(--blue); }
  .err { color: var(--red); }
  .answer { white-space: pre-wrap; line-height: 1.65; font-size: 14px; }
  .msg { border-left: 3px solid var(--border); padding: 6px 12px; margin-bottom: 10px; }
  .msg.sys { border-left-color: var(--accent); }
  .mh { font-size: 13px; }
  .mc { margin-top: 4px; line-height: 1.5; }
  .ml { color: var(--muted); font-size: 12.5px; line-height: 1.5; margin-left: 4px; }
  .kind { font-size: 11px; border: 1px solid var(--border); border-radius: 8px; padding: 1px 6px; margin: 0 4px; }
  .k-critique, .k-rebuttal { color: var(--accent); }
  .k-verdict { color: var(--blue); }
  .k-conclusion { color: var(--green); }
  .speaking { position: sticky; top: -14px; background: var(--panel2); border-radius: 6px; padding: 6px 10px; margin: -4px 0 10px; font-size: 13px; color: var(--muted); }
  .who { color: var(--accent); font-weight: 600; margin-left: 8px; }
  .dots { margin-left: 6px; animation: blink 1.2s infinite; }
  @keyframes blink { 50% { opacity: .2; } }
</style>
