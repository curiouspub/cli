import { defineConfig } from 'astro/config';
import someIntegration from 'some-integration';

export default defineConfig({
  integrations: [someIntegration({ srcDir: './wrong' })],
});
