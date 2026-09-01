import { formatISO } from 'date-fns';
import type { PageServerLoad } from './$types';

// An SSR route with a real dependency, so the image has to actually ship
// node_modules content rather than only static output.
export const load: PageServerLoad = () => ({
	rendered: formatISO(new Date())
});
