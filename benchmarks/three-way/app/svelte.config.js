import adapter from '@sveltejs/adapter-node';

/** @type {import('@sveltejs/kit').Config} */
export default {
	kit: {
		adapter: adapter(),

		// Pinned so two builds of the same commit produce identical client
		// chunk names. SvelteKit's default is Date.now(), which renames every
		// hashed asset on every build — see the repo README's reproducibility
		// note. Pinning it here keeps the comparison fair: all three builds
		// get the same treatment, so any digest instability that remains is
		// the packaging step's, not SvelteKit's.
		version: {
			name: process.env.SOURCE_DATE_EPOCH ?? 'benchmark'
		}
	}
};
