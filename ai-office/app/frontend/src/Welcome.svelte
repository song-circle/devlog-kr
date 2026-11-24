<script lang="ts">
  import { onMount } from 'svelte'
  import { ForgetWorkspace, NewWindow, OpenFolder, OpenWorkspace, RecentWorkspaces } from '../wailsjs/go/main/App'
  import type { store } from '../wailsjs/go/models'

  let { onopened }: { onopened: () => void } = $props()

  let recent = $state<store.Workspace[]>([])
  let error = $state('')

  async function load() {
    try {
      recent = (await RecentWorkspaces()) ?? []
    } catch (e) {
      error = String(e)
    }
  }
  onMount(load)

  async function run(fn: () => Promise<unknown>, after?: () => void) {
    error = ''
    try {
      const r = await fn()
      if (r !== false) after?.()
    } catch (e) {
      error = String(e)
    }
  }

  async function forget(w: store.Workspace) {
    if (!confirm(`최근 목록에서 "${w.name}"을(를) 지울까요? 폴더와 설정, 요청 기록은 그대로 둡니다.`)) return
    await run(() => ForgetWorkspace(w.id), load)
  }

  const when = (s: string) => s.slice(0, 16).replace('T', ' ')
</script>

<div class="welcome">
  <h1>AI Office</h1>
  <p class="hint">프로젝트 폴더(git 저장소)를 열어서 시작합니다. 폴더 여러 개를 한 작업공간으로 묶을 수 있고, 창마다 다른 작업공간을 열 수 있습니다.</p>

  <div class="start">
    <button class="primary" onclick={() => run(OpenFolder, onopened)}>폴더 열기</button>
    <button onclick={() => run(() => NewWindow(0))}>새 창</button>
  </div>
  {#if error}<p class="err">{error}</p>{/if}

  <h2>최근 작업공간</h2>
  {#each recent as w (w.id)}
    <div class="ws">
      <button class="open" onclick={() => run(() => OpenWorkspace(w.id), onopened)}>
        <b>{w.name}</b>
        <span class="folders">{w.projects.map((p) => p.repo).join(' · ') || '폴더 없음'}</span>
      </button>
      <span class="when">{when(w.openedAt)}</span>
      <button onclick={() => run(() => NewWindow(w.id))} title="새 창에서 열기">새 창</button>
      <button class="danger" onclick={() => forget(w)} title="최근 목록에서 지우기">×</button>
    </div>
  {:else}
    <p class="muted">아직 연 작업공간이 없습니다.</p>
  {/each}
</div>

<style>
  .welcome { max-width: 820px; margin: 40px auto; }
  h1 { margin: 0 0 8px; font-size: 26px; color: var(--accent); }
  h2 { margin: 28px 0 10px; font-size: 16px; }
  .hint { color: var(--muted); line-height: 1.5; margin: 0 0 18px; }
  .start { display: flex; gap: 8px; }
  .ws { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
  .open { flex: 1; text-align: left; display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .folders { font-family: var(--mono); font-size: 12px; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .when { font-family: var(--mono); font-size: 11px; color: var(--muted); white-space: nowrap; }
  .muted { color: var(--muted); }
  .err { color: var(--red); }
</style>
