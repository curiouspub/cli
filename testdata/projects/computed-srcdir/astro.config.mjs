import { defineConfig } from 'astro/config';

// srcDir is computed rather than written as a plain string, so a reader
// that is not an interpreter cannot know where the pages live. There is
// no src/pages here either: together those are the case where guessing
// would make a working project undeployable, so the check warns and asks
// instead of stopping.
const root = process.env.SITE_ROOT || 'src';

export default defineConfig({
  srcDir: root,
});
