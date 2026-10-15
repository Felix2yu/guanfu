<script lang="ts">
  // SvelteKit 5 根布局：引入全局样式（Tailwind v4 入口）。
  // 漏掉这个文件 = app.css 从不被打包 = Tailwind 零产出（踩坑记录）。
  import '../app.css';

  let { children } = $props();
</script>

{@render children()}
