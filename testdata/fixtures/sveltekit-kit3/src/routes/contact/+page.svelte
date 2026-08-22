<script lang="ts">
	import { submitContact } from './contact.remote';
</script>

<h1>contact</h1>
<form {...submitContact}>
	<input name="name" placeholder="your name" />
	<button>send</button>
</form>

{#if submitContact.result}
	<p>{submitContact.result.received}</p>
{/if}
