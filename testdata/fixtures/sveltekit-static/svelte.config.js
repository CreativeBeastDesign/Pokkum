import adapter from '@sveltejs/adapter-static';

export default {
	kit: {
		adapter: adapter(),

		// SvelteKit's kit.version.name defaults to Date.now(), which lands in
		// client/_app/version.json. Pinning it from SOURCE_DATE_EPOCH keeps a
		// static build byte-identical across runs for the same epoch, matching
		// testdata/fixtures/sveltekit-basic/svelte.config.js's convention.
		// Configuring the adapter here (rather than via vite.config.ts's
		// sveltekit() options, as `sv create` scaffolds it by default) is
		// deliberate: SvelteKit ignores svelte.config.js entirely once
		// vite.config.ts passes adapter options directly to the sveltekit()
		// plugin, which would silently defeat this pin.
		version: {
			name: process.env.SOURCE_DATE_EPOCH ?? 'dev'
		}
	}
};
