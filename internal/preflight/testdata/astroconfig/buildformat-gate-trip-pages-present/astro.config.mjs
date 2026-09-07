const ANALYTICS_ID = 'G-EXAMPLE00';

function analytics() {
  return {
    name: 'example:analytics',
    hooks: {
      'astro:config:setup': ({ injectScript }) => {
        injectScript('head-inline', `gtag('config','${ANALYTICS_ID}');`);
      },
    },
  };
}

export default {
  integrations: [analytics()],
};
