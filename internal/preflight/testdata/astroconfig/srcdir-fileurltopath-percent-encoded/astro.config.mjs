import { fileURLToPath } from 'node:url';

export default {
  srcDir: fileURLToPath(new URL('./source%20files', import.meta.url)),
};
