<script lang="ts">
	let { data } = $props();
</script>

<h1>Pokkum benchmark app</h1>
<p>A deliberately small but realistic SvelteKit app: one SSR route, one
prerendered route, one API endpoint, and one real npm dependency.</p>
<p>Rendered at: {data.rendered}</p>
<p><a href="/about">About (prerendered)</a> · <a href="/api/hello">API</a></p>
