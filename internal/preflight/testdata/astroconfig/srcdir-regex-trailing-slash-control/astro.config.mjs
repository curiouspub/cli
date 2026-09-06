export default {
  integrations: [compress({ exclude: 'blog' })],
  build: { format: 'file' },
};
