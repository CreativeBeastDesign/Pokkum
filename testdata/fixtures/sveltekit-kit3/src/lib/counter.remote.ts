import { command, query } from '$app/server';

let count = 0;

export const getCount = query(async () => count);

export const increment = command(async () => {
	count += 1;
	return count;
});
