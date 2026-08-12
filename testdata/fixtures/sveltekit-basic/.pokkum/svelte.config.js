import adapter from '@jesterkit/exe-sveltekit';

export default {
  kit: {
    adapter: adapter({ binaryName: 'server', out: 'dist', target: 'linux-x64' }),

    // SvelteKit's kit.version.name defaults to Date.now(), which lands in
    // client/_app/version.json. The exe adapter embeds the client assets into
    // the compiled binary, so leaving this at its default makes every build
    // byte-different and defeats reproducible images entirely. Pokkum exports
    // SOURCE_DATE_EPOCH into the build environment for exactly this purpose.
    version: {
      name: process.env.SOURCE_DATE_EPOCH ?? 'dev'
    }
  }
};
