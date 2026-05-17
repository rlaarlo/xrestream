<script lang="ts">
  import Icon from '@iconify/svelte';
  import { onMount } from 'svelte';
  import { themeMode, toggleTheme, applyTheme } from '$lib/theme';

  export let size: 'xs' | 'sm' | 'md' = 'sm';

  onMount(() => {
    // Ensure DOM matches store on first paint after hydration.
    applyTheme($themeMode);
  });

  $: isDark = $themeMode === 'dark';
  $: btnSize = size === 'md' ? '' : `btn-${size}`;
</script>

<button
  type="button"
  class="btn btn-ghost btn-square {btnSize}"
  aria-label={isDark ? 'Switch to light theme' : 'Switch to dark theme'}
  title={isDark ? 'Light mode' : 'Dark mode'}
  on:click={toggleTheme}
>
  {#if isDark}
    <Icon icon="lucide:sun" class="text-lg" />
  {:else}
    <Icon icon="lucide:moon" class="text-lg" />
  {/if}
</button>
