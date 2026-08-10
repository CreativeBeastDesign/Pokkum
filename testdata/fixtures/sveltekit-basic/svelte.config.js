import adapter from '@jesterkit/exe-sveltekit';

export default {
  kit: {
    adapter: adapter({ binaryName: 'server', out: 'dist', target: 'linux-x64' })
  }
};
