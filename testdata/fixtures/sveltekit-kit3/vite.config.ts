import adapter from '@sveltejs/adapter-node';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			// SvelteKit 3 has no svelte.config.js (see this fixture's README) so
			// this is the only place the adapter is configured -- and because it
			// is already correct here, Pokkum's Prepare takes the
			// PrepareVirtualViteConfigPassthrough path rather than
			// PrepareVirtualViteConfig, which is the only one of the two that
			// calls injectViteVersionPin. Passthrough does not pin kit.version on
			// its own, so a project like this one must pin it itself for a
			// reproducible build -- exactly as sveltekit-basic's
			// svelte.config.js already does for its own (different) adapter.
			// Left out, two builds of identical source diverge (SvelteKit falls
			// back to Date.now(), see Lessons.md's 2026-08-21 "vite.config.ts
			// reproducibility" entry, which fixed the sibling gap on the
			// injection path, not this one).
			version: { name: process.env.SOURCE_DATE_EPOCH || 'pokkum-reproducible-build' },
			adapter: adapter(),
			experimental: {
				remoteFunctions: true
			}
		})
	]
});
