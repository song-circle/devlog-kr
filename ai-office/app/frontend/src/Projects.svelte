<script lang="ts">
  import { onMount } from 'svelte'
  import { AddFolder, Projects, Release, RemoveFolder, SaveProject } from '../wailsjs/go/main/App'
  import type { store } from '../wailsjs/go/models'
  import Knowledge from './Knowledge.svelte'

  let { onchanged, onstarted, draft, ondraftdone }: {
    onchanged: () => void; onstarted: (id: string) => void
    draft: { projectId: number; text: string } | null; ondraftdone: () => void
  } = $props()

  // Folder whose knowledge document is open, and the AI draft to review in it.
  let knowledgeOf = $state<store.Project | null>(null)
  let knowledgeDraft = $state<string | null>(null)

  let list = $state<store.Project[]>([])
  let editing = $state<any | null>(null)
  let error = $state('')

  async function load() {
    try {
      list = (await Projects()) ?? []
    } catch (e) {
      error = String(e)
    }
  }
  onMount(async () => {
    await load()
    if (draft) {
      knowledgeOf = list.find((p) => p.id === draft.projectId) ?? null
      knowledgeDraft = draft.text
      editing = null
      ondraftdone()
    }
  })

  function openKnowledge(p: store.Project) {
    editing = null
    knowledgeDraft = null
    knowledgeOf = p
  }

  function edit(p: store.Project) {
    error = ''
    knowledgeOf = null
    editing = {
      ...p, checksText: (p.checks ?? []).join('\n'), protectText: (p.protect ?? []).join(', '),
      copyText: (p.copyFiles ?? []).join('\n'), setupText: (p.setup ?? []).join('\n'),
    }
  }

  async function add() {
    error = ''
    try {
      if (await AddFolder()) {
        await load()
        onchanged()
      }
    } catch (e) {
      error = String(e)
    }
  }

  async function save() {
    error = ''
    try {
      await SaveProject({
        id: editing.id, name: editing.name, repo: editing.repo, notes: editing.notes,
        checks: editing.checksText.split('\n'), protect: editing.protectText.split(','),
        copyFiles: editing.copyText.split('\n'), setup: editing.setupText.split('\n'),
        devBranch: editing.devBranch ?? '', prodBranch: editing.prodBranch ?? '',
      } as any)
      editing = null
      await load()
      onchanged()
    } catch (e) {
      error = String(e)
    }
  }

  // Production release: a GitHub pull request dev → prod.
  let releasing = $state(0)
  let released = $state<Record<number, string>>({})
  async function release(p: store.Project) {
    error = ''
    releasing = p.id
    try {
      const r = await Release(p.id)
      released[p.id] = (r.created ? '운영 반영 요청(PR)을 만들었습니다' : '이미 열려 있는 운영 반영 요청이 있습니다') +
        ` — 변경 ${r.changes?.length ?? 0}건. 브라우저에서 열었습니다: ${r.url}`
    } catch (e) {
      error = String(e)
    } finally {
      releasing = 0
    }
  }

  async function remove(p: store.Project) {
    if (!confirm(`${p.name} 폴더를 이 작업공간에서 뺄까요? 폴더의 파일과 설정, 요청 기록은 그대로 둡니다.`)) return
    error = ''
    try {
      await RemoveFolder(p.id)
      await load()
      onchanged()
    } catch (e) {
      error = String(e)
    }
  }
</script>

<div class="top">
  <div>
    <h1>폴더 설정</h1>
    <p class="hint">이 작업공간에 들어 있는 프로젝트 폴더입니다. 폴더마다 검사 명령, 고칠 수 없는 파일, <b>프로젝트 설명</b>(기술 스택, 지켜야 할 규칙, 배경)을 정합니다. 프로젝트 설명은 모든 직원의 지시에 자동으로 붙습니다.</p>
  </div>
  <button class="primary" onclick={add}>+ 폴더 추가</button>
</div>
{#if error}<p class="err">{error}</p>{/if}

{#if knowledgeOf}
  {#key knowledgeOf.id}
    <Knowledge project={knowledgeOf} draft={knowledgeDraft} onclose={() => { knowledgeOf = null; knowledgeDraft = null }} {onstarted} />
  {/key}
{/if}

{#if editing}
  <div class="card edit">
    <div class="grid2">
      <div>
        <label for="pn">이름</label>
        <input id="pn" bind:value={editing.name} />
      </div>
      <div>
        <span class="lbl">폴더 (git 저장소)</span>
        <div class="mono path">{editing.repo}</div>
      </div>
    </div>
    <div class="grid2">
      <div>
        <label for="pc">검사 명령 (한 줄에 하나, 코드 수정 요청에서 모두 통과해야 완료)</label>
        <textarea id="pc" rows="3" bind:value={editing.checksText} placeholder="go test ./..."></textarea>
      </div>
      <div>
        <label for="pp">AI가 고칠 수 없는 파일 (쉼표로 구분)</label>
        <input id="pp" bind:value={editing.protectText} placeholder="*_test.go" />
      </div>
    </div>
    <div class="prep">
      <b>반영</b>
      <span class="muted">개발 브랜치를 적으면 AI 작업은 GitHub(origin)의 최신 개발 브랜치에서 시작하고, 승인하면 최신 개발 브랜치와 합쳐 검사를 다시 돌린 뒤 push합니다(누구나). 운영 브랜치에는 직접 반영하지 않고 GitHub PR을 만듭니다 — 승인은 GitHub에서. 비우면 이 PC의 저장소에 바로 merge합니다.</span>
    </div>
    <div class="grid2">
      <div>
        <label for="pd">개발 브랜치</label>
        <input id="pd" bind:value={editing.devBranch} placeholder="dev (비우면 이 PC에 바로 반영)" />
      </div>
      <div>
        <label for="pm">운영 브랜치</label>
        <input id="pm" bind:value={editing.prodBranch} placeholder="main" />
      </div>
    </div>
    <div class="prep">
      <b>작업 폴더 준비</b>
      <span class="muted">AI는 요청마다 git에 있는 파일만 든 별도 폴더에서 일합니다. 빌드·검사에 필요한데 git에 없는 파일은 여기서 채웁니다. 코드 수정 요청에서만, AI가 일을 시작하기 전에 실행합니다.</span>
    </div>
    <div class="grid2">
      <div>
        <label for="pf">복사할 파일·폴더 (한 줄에 하나, 프로젝트 폴더 기준, * 사용 가능)</label>
        <textarea id="pf" rows="3" bind:value={editing.copyText} placeholder={'.env\nconfig/local.json'}></textarea>
        <span class="small muted">git이 무시하는(.gitignore에 있는) 파일만 복사합니다. 복사한 파일은 AI도 읽을 수 있습니다.</span>
      </div>
      <div>
        <label for="ps">준비 명령 (한 줄에 하나, 차례로 실행, 각 15분 제한)</label>
        <textarea id="ps" rows="3" bind:value={editing.setupText} placeholder={'npm ci\ngo mod download'}></textarea>
        <span class="small muted">실패하거나 git에 잡히는 파일을 바꾸면 요청을 멈춥니다(AI 변경에 섞이지 않게).</span>
      </div>
    </div>
    <label for="no">프로젝트 설명 (모든 직원에게 전달)</label>
    <textarea id="no" rows="5" bind:value={editing.notes} placeholder={'예:\n- Go 1.22, 표준 라이브러리만 사용\n- 금액은 정수 센트로 다룬다\n- 사용자 화면 문구는 한국어'}></textarea>
    <div class="actions">
      <button class="primary" onclick={save}>저장</button>
      <button onclick={() => (editing = null)}>취소</button>
    </div>
  </div>
{/if}

<div class="cards">
  {#each list as p (p.id)}
    <div class="card">
      <div class="row">
        <div><b class="name">{p.name}</b> <span class="mono muted">{p.repo}</span></div>
        <div class="btns">
          {#if p.devBranch}<button disabled={releasing === p.id} onclick={() => release(p)}>{releasing === p.id ? '확인 중…' : '운영 반영 요청'}</button>{/if}
          <button onclick={() => openKnowledge(p)}>지식 문서</button>
          <button onclick={() => edit(p)}>설정</button>
          {#if list.length > 1}<button class="danger" onclick={() => remove(p)}>작업공간에서 빼기</button>{/if}
        </div>
      </div>
      <div class="meta">
        <span>검사: {p.checks?.length ? p.checks.join(' · ') : '없음'}</span>
        <span>보호: {p.protect?.length ? p.protect.join(', ') : '없음'}</span>
        <span>반영: {p.devBranch ? `origin/${p.devBranch} → ${p.prodBranch}(PR)` : '이 PC에 바로'}</span>
        <span>준비: {[...(p.copyFiles ?? []).map((f) => '복사 ' + f), ...(p.setup ?? [])].join(' · ') || '없음'}</span>
      </div>
      {#if released[p.id]}<p class="ok">{released[p.id]}</p>{/if}
      {#if p.notes}<pre class="notes">{p.notes}</pre>{/if}
    </div>
  {/each}
</div>

<style>
  .top { display: flex; justify-content: space-between; align-items: flex-start; gap: 20px; }
  .top .primary { white-space: nowrap; }
  h1 { margin: 0 0 6px; font-size: 22px; }
  .hint { color: var(--muted); margin: 0 0 14px; line-height: 1.5; max-width: 900px; }
  .cards { display: grid; gap: 12px; }
  .card { background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 14px 16px; }
  .card.edit { border-color: var(--accent); margin-bottom: 16px; }
  .row { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
  .btns { display: flex; gap: 6px; }
  .name { font-size: 16px; margin-right: 8px; }
  .mono { font-family: var(--mono); font-size: 12px; }
  .path { padding: 8px 0; }
  .lbl { display: block; color: var(--muted); font-size: 12px; margin: 14px 0 6px; }
  .muted { color: var(--muted); }
  .meta { display: flex; gap: 24px; color: var(--muted); font-size: 12px; margin-top: 8px; }
  .notes { margin-top: 10px; color: var(--text); background: var(--panel2); border-radius: 6px; padding: 8px 10px; }
  .grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
  .prep { margin-top: 18px; display: flex; flex-direction: column; gap: 4px; }
  .prep .muted, .small { font-size: 12px; line-height: 1.5; }
  .actions { margin-top: 14px; display: flex; gap: 8px; }
  .err { color: var(--red); }
  .ok { color: var(--green); font-size: 13px; margin: 8px 0 0; word-break: break-all; }
</style>
