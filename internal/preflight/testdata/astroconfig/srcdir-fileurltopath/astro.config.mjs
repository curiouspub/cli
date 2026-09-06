import { fileURLToPath } from 'node:url';

export default {
  srcDir: fileURLToPath(new URL('./source', import.meta.url)),
};
