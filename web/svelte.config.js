import adapter from '@sveltejs/adapter-static';

// 观复前端是纯 SPA：所有数据来自同源 /api/v1，无 SSR 需求。
// fallback: 'index.html' 让任意路径都回退到入口（history 路由）。
export default {
  kit: {
    adapter: adapter({
      pages: 'build',
      assets: 'build',
      fallback: 'index.html',
      precompress: false,
      strict: true
    })
  }
};
