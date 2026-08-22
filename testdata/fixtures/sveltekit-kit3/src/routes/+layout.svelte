<script lang="ts">
	let { children } = $props();
</script>

<nav>
	<a href="/">home</a>
	<a href="/about">about</a>
	<a href="/contact">contact</a>
	<a href="/remotes">remotes</a>
</nav>

{@render children?.()}
