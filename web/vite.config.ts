import adapter from '@sveltejs/adapter-static';
import tailwindcss from '@tailwindcss/vite';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// 观复前端是纯 SPA：所有数据来自同源 /api/v1，无 SSR 需求。
// SvelteKit 3 起不再读取 svelte.config.js（`kit/config_file_unsupported`），
// adapter 等配置一律经 vite.config.ts 的 sveltekit() 插件传入。
// fallback: 'index.html' 让任意路径都回退到入口（history 路由）。
//
// Kit 3 移除了内置 `$lib`，改用 package.json 的 subpath imports（`#lib/*`）。
// 注意 `config.alias` 虽然也能把 `$lib` 接回来，但它已被 Kit 标为 deprecated
// （每次 check/build 都告警），所以走 `#lib` 正规写法；代价是导入必须带扩展名
// ——subpath imports 按 Node 规范映射到**确切文件**，不猜扩展名，
// 于是 `#lib/api` 要写成 `#lib/api.ts`，并开 allowImportingTsExtensions（见 tsconfig.json）。
export default defineConfig({
  plugins: [
    tailwindcss(),
    sveltekit({
      adapter: adapter({
        pages: 'build',
        assets: 'build',
        fallback: 'index.html',
        precompress: false,
        strict: true
      })
    })
  ],
  // 开发期 API 代理到本地 Go 服务（生产由 Go 单二进制同源托管，无需代理）。
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8788',
        changeOrigin: true
      }
    }
  }
});
