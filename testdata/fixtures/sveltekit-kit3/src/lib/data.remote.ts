import { query, prerender } from '$app/server';

export const getMessage = query(async () => {
	return `hello from a remote query, t=${Date.now()}`;
});

export const getBuildInfo = prerender(async () => {
	return { generator: 'sk3probe', when: 'build-time' };
});
