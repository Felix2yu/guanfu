<script lang="ts">
  // SvelteKit 5 根布局：引入全局样式（Tailwind v4 入口）+ 应用外壳 + 界面偏好初始化。
  // 漏掉 app.css = Tailwind 零产出（踩坑记录）。
  // initUI 放在这里而不是各页面：主题与视图偏好是全局的，页面各初始化一遍只会互相覆盖。
  import '../app.css';
  import { onMount } from 'svelte';
  import { disposeUI, initUI } from '#lib/ui.svelte.ts';
  import Shell from '#lib/components/Shell.svelte';

  let { children } = $props();

  onMount(() => {
    initUI();
    return disposeUI;
  });
</script>

<Shell>
  {@render children()}
</Shell>
