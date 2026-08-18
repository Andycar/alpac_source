<script lang="ts">
  /**
   * BookmarksPanel — list / edit / jump-to bookmarks for the current URL.
   * Opens via 'B' shortcut (long-press) or from SettingsMenu action.
   */

  import { engine } from '../stores/player';
  import { currentBookmarks, removeBookmark, updateBookmarkNote, clearBookmarks } from '../stores/bookmarks';
  import { activeMenu } from '../stores/ui';
  import { formatTime } from '../utils/time';

  let editingId = $state<number | null>(null);
  let draftNote = $state('');

  function jump(t: number) {
    $engine?.seek(t);
    activeMenu.set('none');
  }

  function startEdit(b: { createdAt: number; note: string }) {
    editingId = b.createdAt;
    draftNote = b.note;
  }
  function commitEdit() {
    if (editingId !== null) {
      updateBookmarkNote(editingId, draftNote);
      editingId = null;
    }
  }
  function cancelEdit() { editingId = null; }
</script>

<div class="lwp-bookmarks-panel">
  {#if $currentBookmarks.length === 0}
    <div class="lwp-bookmarks-empty">
      Нет закладок. Нажмите <kbd>B</kbd>, чтобы добавить текущий момент.
    </div>
  {:else}
    <div class="lwp-bookmarks-list">
      {#each $currentBookmarks as b (b.createdAt)}
        <div class="lwp-bookmark-row">
          <button class="lwp-btn lwp-btn-ghost lwp-bookmark-time"
            onclick={() => jump(b.time)}
            aria-label="Перейти к моменту"
          >{formatTime(b.time)}</button>
          {#if editingId === b.createdAt}
            <input
              class="lwp-bookmark-note-input"
              type="text"
              bind:value={draftNote}
              onkeydown={(e) => { if (e.key === 'Enter') commitEdit(); if (e.key === 'Escape') cancelEdit(); }}
              maxlength="80"
            />
            <button class="lwp-btn lwp-btn-primary" onclick={commitEdit}>OK</button>
            <button class="lwp-btn lwp-btn-ghost" onclick={cancelEdit}>×</button>
          {:else}
            <div class="lwp-bookmark-label" onclick={() => startEdit(b)}>
              {b.label}
            </div>
            <button class="lwp-btn lwp-btn-ghost"
              onclick={() => removeBookmark(b.createdAt)}
              aria-label="Удалить"
            >×</button>
          {/if}
        </div>
      {/each}
    </div>
    <div class="lwp-bookmarks-footer">
      <button class="lwp-btn lwp-btn-danger" onclick={clearBookmarks}>Очистить все</button>
    </div>
  {/if}
</div>
