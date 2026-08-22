<script lang="ts">
	import { getMessage, getBuildInfo } from '#lib/data.remote';
	import { getCount, increment } from '#lib/counter.remote';

	const message = getMessage();
	const info = getBuildInfo();
</script>

<h1>remotes</h1>

{#await message then m}
	<p>{m}</p>
{/await}

{#await info then i}
	<pre>{JSON.stringify(i)}</pre>
{/await}

{#await getCount() then c}
	<p>count: {c}</p>
{/await}

<form {...increment}>
	<button>increment</button>
</form>
