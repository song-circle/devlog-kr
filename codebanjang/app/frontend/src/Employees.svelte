<script lang="ts">
  import { onMount } from 'svelte'
  import { CancelLogin, DeleteEmployee, Employees, Login, LoginCode, Logout, Models, Ollama, OllamaProbes, ProbeModel, SaveEmployee } from '../wailsjs/go/main/App'
  import { EventsOn } from '../wailsjs/runtime/runtime'
  import type { main } from '../wailsjs/go/models'

  let list = $state<main.EmployeeView[]>([])
  let models = $state<string[]>([])
  let local = $state<{ running: boolean; version: string; models: { name: string; sizeGb: number; params: string }[]; error?: string } | null>(null)
  let loading = $state(true)
  let error = $state('')
  let editing = $state<any | null>(null) // employee being edited (copy)
  type Probe = { model: string; toolCalls: number; readFile: boolean; json: boolean; seconds: number; note?: string }
  let probes = $state<Record<string, Probe>>({})
  let probing = $state<Record<string, boolean>>({})
  const probeOf = (m: string) => probes[m] ?? probes[m + ':latest'] ?? probes[m.replace(/:latest$/, '')]
  function probeText(m: string) {
    const r = probeOf(m)
    if (!r) return '시험 안 함'
    if (r.readFile && r.json) return `사용 가능 (${r.seconds.toFixed(0)}초)`
    if (!r.toolCalls) return '불가: 파일을 읽지 못함(도구 호출 안 함)'
    if (!r.readFile) return '불가: 도구는 불렀지만 파일 내용을 가져오지 못함'
    if (!r.json) return '불안정: 정해진 형식으로 답하지 못함'
    return '불가: ' + (r.note ?? '')
  }
  async function probe(m: string) {
    probing[m] = true
    try {
      const r = await ProbeModel(m)
      probes[m] = r as any
    } catch (e) {
      error = String(e)
    } finally {
      probing[m] = false
    }
  }
  let modelChoices = $derived(editing?.connection === 'ollama' ? (local?.models ?? []).map((m) => m.name) : models)
  let loginLines = $state<Record<number, string[]>>({})
  let codes = $state<Record<number, string>>({})

  const roleName: Record<string, string> = { pm: 'PM', dev: '개발자', qa: 'QA' }

  async function load() {
    loading = true
    try {
      list = (await Employees()) ?? []
      error = ''
    } catch (e) {
      error = String(e)
    } finally {
      loading = false
    }
  }

  onMount(() => {
    load()
    Models().then((m) => (models = m ?? []))
    Ollama().then((s) => (local = s as any))
    OllamaProbes().then((p) => (probes = (p ?? {}) as any))
    const off1 = EventsOn('login', (ev: { id: number; line: string }) => {
      loginLines[ev.id] = [...(loginLines[ev.id] ?? []), ev.line]
    })
    const off2 = EventsOn('login-done', () => load())
    return () => {
      off1()
      off2()
    }
  })

  function newEmployee() {
    editing = { id: 0, name: '', role: 'dev', model: 'sonnet', prompt: '', connection: 'default', authMode: 'subscription' }
  }

  async function save() {
    try {
      await SaveEmployee(editing)
      editing = null
      await load()
    } catch (e) {
      error = String(e)
    }
  }

  async function run(fn: () => Promise<any>) {
    error = ''
    try {
      await fn()
    } catch (e) {
      error = String(e)
    }
    await load()
  }

  async function login(id: number) {
    loginLines[id] = []
    await run(() => Login(id))
  }

  // Claude Code prints a fallback link when the browser does not open.
  const linkOf = (lines: string[] = []) => lines.map((l) => l.match(/https:\/\/\S+/)?.[0]).find(Boolean)

  // Show only enough of the address to tell accounts apart (screens get recorded).
  function mask(email: string) {
    const [user, domain] = email.split('@')
    if (!domain) return '***'
    return user.slice(0, 2) + '***@' + domain
  }

  function statusText(e: main.EmployeeView) {
    const s = e.status
    if (e.loggingIn) return '로그인 진행 중…'
    if (s?.error) return '확인 실패: ' + s.error
    if (e.connection === 'ollama') return s?.loggedIn ? `${s.plan} · ${e.model} · ${probeText(e.model)}` : '사용 불가'
    if (!s?.loggedIn) return '로그인 안 됨'
    const kind = s.authMethod === 'claude.ai' ? `구독${s.plan ? ' (' + s.plan + ')' : ''}` : s.authMethod === 'console' ? 'Console(API 과금)' : s.authMethod
    return `${kind}${s.email ? ' · ' + mask(s.email) : ''}`
  }
</script>

<div class="top">
  <div>
    <h1>직원</h1>
    <p class="hint">직원마다 모델과 AI 계정을 따로 정합니다. <b>전용 계정</b>은 그 직원만의 Claude Code 로그인을 쓰고, 로그인은 Claude Code의 공식 브라우저 로그인으로 합니다(앱은 계정 정보를 저장하지 않습니다).</p>
  </div>
  <button class="primary" onclick={newEmployee}>+ 직원 추가</button>
</div>

{#if error}<p class="err">{error}</p>{/if}

{#if editing}
  <div class="card edit">
    <h3>{editing.id ? '직원 수정' : '새 직원'}</h3>
    <div class="grid">
      <div>
        <label for="n">이름</label>
        <input id="n" bind:value={editing.name} placeholder="예: 김개발" />
      </div>
      <div>
        <label for="r">역할</label>
        <select id="r" bind:value={editing.role}>
          <option value="pm">PM (계획, 읽기 전용)</option>
          <option value="dev">개발자 (파일 수정)</option>
          <option value="qa">QA (리뷰, 읽기 전용)</option>
        </select>
      </div>
      <div>
        <label for="m">모델</label>
        <input id="m" list="models" bind:value={editing.model} />
        <datalist id="models">{#each modelChoices as m}<option value={m}></option>{/each}</datalist>
      </div>
    </div>
    <span class="lbl">AI 계정 연결</span>
    <div class="conn">
      <button class:on={editing.connection === 'default'} onclick={() => (editing.connection = 'default')}>
        <b>이 PC 기본 로그인</b><span>지금 PC의 Claude Code에 로그인된 계정을 그대로 씀</span>
      </button>
      <button class:on={editing.connection === 'own' && editing.authMode === 'subscription'} onclick={() => { editing.connection = 'own'; editing.authMode = 'subscription' }}>
        <b>전용 계정 · 구독</b><span>이 직원만의 Claude 구독 로그인 (개인 사용)</span>
      </button>
      <button class:on={editing.connection === 'own' && editing.authMode === 'console'} onclick={() => { editing.connection = 'own'; editing.authMode = 'console' }}>
        <b>전용 계정 · Console</b><span>Anthropic Console 로그인, API 사용량으로 과금</span>
      </button>
      <button class:on={editing.connection === 'ollama'} onclick={() => { editing.connection = 'ollama'; if (local?.models?.length && !local.models.some((m) => m.name === editing.model)) editing.model = local.models[0].name }}>
        <b>Ollama · 이 PC</b><span>이 PC에서 도는 공개 모델. 계정·비용 없음, 코드가 PC 밖으로 나가지 않음</span>
      </button>
    </div>
    {#if editing.connection === 'ollama'}
      <p class="small">
        {#if !local?.running}Ollama가 실행 중이 아닙니다. Ollama를 켠 뒤 다시 열어 주세요.
        {:else if !local.models.length}설치된 모델이 없습니다. 터미널에서 <span class="mono">ollama pull qwen3:8b</span> 처럼 받으세요.
        {:else}직원으로 쓰려면 파일을 직접 읽고(도구 호출) 정해진 형식으로 답할 수 있어야 합니다.{/if}
      </p>
      {#if local?.models?.length}
        <div class="probes">
          {#each local.models as m}
            {@const r = probeOf(m.name)}
            <div class="probe" class:ok={r?.readFile && r?.json} class:bad={r && !(r.readFile && r.json)}>
              <button class="pick" onclick={() => (editing.model = m.name)} class:on={editing.model === m.name}>{m.name}</button>
              <span class="muted">{m.params} · {m.sizeGb.toFixed(1)}GB</span>
              <span class="pr">{probing[m.name] ? '시험 중… (최대 몇 분)' : probeText(m.name)}</span>
              <button class="mini" disabled={probing[m.name]} onclick={() => probe(m.name)}>{r ? '다시 시험' : '모델 시험'}</button>
            </div>
          {/each}
        </div>
      {/if}
    {/if}
    <label for="p">추가 지시 (선택, 역할 기본 지시 뒤에 붙음)</label>
    <textarea id="p" rows="3" bind:value={editing.prompt} placeholder="예: 주석과 커밋 설명은 한국어로 쓴다"></textarea>
    <div class="actions">
      <button class="primary" onclick={save}>저장</button>
      <button onclick={() => (editing = null)}>취소</button>
    </div>
  </div>
{/if}

{#if loading && list.length === 0}<p class="muted">직원과 로그인 상태를 확인하는 중…</p>{/if}

<div class="cards">
  {#each list as e (e.id)}
    <div class="card">
      <div class="row">
        <div>
          <span class="role r-{e.role}">{roleName[e.role]}</span>
          <b class="name">{e.name}</b>
          <span class="mono muted">{e.model}</span>
        </div>
        <div class="btns">
          <button onclick={() => (editing = { ...e })}>수정</button>
          <button class="danger" onclick={() => confirm(`${e.name} 직원을 삭제할까요?`) && run(() => DeleteEmployee(e.id))}>삭제</button>
        </div>
      </div>
      <div class="row acct">
        <div>
          <span class="dot" class:ok={e.status?.loggedIn}></span>
          <span>{e.connection === 'own' ? '전용 계정' : e.connection === 'ollama' ? 'Ollama(이 PC)' : '이 PC 기본 로그인'} — {statusText(e)}</span>
        </div>
        {#if e.connection === 'own'}
          <div class="btns">
            {#if e.loggingIn}
              <button onclick={() => run(async () => CancelLogin(e.id))}>로그인 취소</button>
            {:else if e.status?.loggedIn}
              <button onclick={() => run(() => Logout(e.id))}>로그아웃</button>
            {:else}
              <button class="ok" onclick={() => login(e.id)}>로그인</button>
            {/if}
          </div>
        {/if}
      </div>
      {#if e.loggingIn}
        <div class="login">
          <p>브라우저에서 로그인을 마치면 자동으로 연결됩니다.</p>
          {#if linkOf(loginLines[e.id])}
            <p class="small">브라우저가 열리지 않았다면: <span class="mono link">{linkOf(loginLines[e.id])}</span></p>
          {/if}
          <div class="code">
            <input placeholder="브라우저 화면에 코드가 나오면 여기에 붙여 넣기" bind:value={codes[e.id]} />
            <button onclick={() => run(() => LoginCode(e.id, codes[e.id] ?? ''))}>코드 보내기</button>
          </div>
        </div>
      {/if}
      {#if e.prompt}<p class="prompt">추가 지시: {e.prompt}</p>{/if}
    </div>
  {/each}
</div>

<style>
  .top { display: flex; justify-content: space-between; align-items: flex-start; gap: 20px; }
  h1 { margin: 0 0 6px; font-size: 22px; }
  h3 { margin: 0 0 4px; }
  .hint { color: var(--muted); margin: 0 0 14px; line-height: 1.5; max-width: 900px; }
  .cards { display: grid; gap: 12px; }
  .card { background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 14px 16px; }
  .card.edit { border-color: var(--accent); margin-bottom: 16px; }
  .row { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
  .row.acct { margin-top: 10px; color: var(--muted); }
  .top .primary { white-space: nowrap; }
  .btns { display: flex; gap: 6px; }
  .name { font-size: 16px; margin: 0 8px; }
  .role { font-size: 12px; padding: 2px 8px; border-radius: 10px; border: 1px solid currentColor; }
  .r-pm { color: var(--blue); }
  .r-dev { color: var(--accent); }
  .r-qa { color: var(--green); }
  .mono { font-family: var(--mono); font-size: 12px; }
  .muted { color: var(--muted); }
  .dot { display: inline-block; width: 8px; height: 8px; border-radius: 4px; background: var(--red); margin-right: 6px; }
  .dot.ok { background: var(--green); }
  .grid { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 14px; }
  .lbl { display: block; color: var(--muted); font-size: 12px; margin: 14px 0 6px; }
  .probes { display: grid; gap: 4px; margin-top: 8px; }
  .probe { display: grid; grid-template-columns: 200px 120px 1fr auto; gap: 10px; align-items: center; font-size: 12.5px; padding: 4px 8px; border-radius: 6px; background: var(--panel2); }
  .probe.ok .pr { color: var(--green); }
  .probe.bad .pr { color: var(--red); }
  .probe .pick { text-align: left; padding: 3px 8px; font-family: var(--mono); font-size: 12px; }
  .probe .pick.on { border-color: var(--accent); }
  .mini { padding: 3px 10px; font-size: 12px; }
  .conn { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 10px; }
  .conn button { text-align: left; display: flex; flex-direction: column; gap: 4px; padding: 10px 12px; }
  .conn span { color: var(--muted); font-size: 12px; }
  .conn button.on { border-color: var(--accent); background: rgba(232, 135, 95, .1); }
  .actions { margin-top: 14px; display: flex; gap: 8px; }
  .login { margin-top: 10px; background: var(--panel2); border-radius: 8px; padding: 10px 12px; }
  .login p { margin: 0 0 6px; }
  .small { font-size: 12px; color: var(--muted); }
  .link { word-break: break-all; color: var(--blue); }
  .code { display: flex; gap: 8px; }
  .code button { white-space: nowrap; }
  .prompt { color: var(--muted); font-size: 12px; margin: 8px 0 0; }
  .err { color: var(--red); }
</style>
