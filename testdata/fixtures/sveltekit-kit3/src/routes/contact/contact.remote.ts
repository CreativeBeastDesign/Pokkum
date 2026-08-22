import { form } from '$app/server';

export const submitContact = form(async (data) => {
	const name = data.get('name');
	return { ok: true, received: String(name ?? '') };
});
