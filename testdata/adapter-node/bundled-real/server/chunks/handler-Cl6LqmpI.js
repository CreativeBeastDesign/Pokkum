// Trimmed excerpt of the real Vite/Rollup-bundled adapter-node handler chunk
// that build/handler.js re-exports from. Only the parts that exercise
// prerendered_patch.go's matcher are kept; see testdata/adapter-node/README.md
// for exact provenance and for what was elided.
import fs__default from 'node:fs';
import path from 'node:path';
import { s as sirv, a as parse } from './vendor.js';
import { env, dir, env_prefix } from '../../env.js';

const prerendered = new Set(["/about","/about.html"]);

const asset_dir = `${dir}/client`;

/**
 * @param {string} path
 * @param {boolean} client
 */
function serve(path, client = false) {
	return fs__default.existsSync(path)
		? sirv(path, {
				etag: true,
				gzip: false,
				brotli: false,
				setHeaders: client
					? (res, pathname) => {
							if (pathname.startsWith('/_app/immutable/') && res.statusCode === 200) {
								res.setHeader('cache-control', 'public,max-age=31536000,immutable');
							}
						}
					: undefined
			})
		: undefined;
}

// required because the static file server ignores trailing slashes
/** @returns {import('polka').Middleware} */
function serve_prerendered() {
	const handler = serve(path.join(dir, 'prerendered'));

	return (req, res, next) => {
		let { pathname, search, query } = parse(req);

		try {
			pathname = decodeURIComponent(pathname);
		} catch {
			// ignore invalid URI
		}

		if (prerendered.has(pathname)) {
			return handler?.(req, res, next);
		}

		// remove or add trailing slash as appropriate
		let location = pathname.at(-1) === '/' ? pathname.slice(0, -1) : pathname + '/';
		if (prerendered.has(location)) {
			if (query) location += search;
			res.writeHead(308, { location }).end();
		} else {
			void next();
		}
	};
}

/** @type {import('polka').Middleware} */
const ssr = async (req, res) => {
	// elided: request construction, origin resolution and error handling
	const response = await server.respond(await getRequest({ base: origin, request: req }), {
		platform: { req },
		getClientAddress: () => req.socket?.remoteAddress
	});

	await setResponse(res, response);
};

/** @param {import('polka').Middleware[]} handlers */
function sequence(handlers) {
	/** @type {import('polka').Middleware} */
	return (req, res, next) => {
		/**
		 * @param {number} i
		 * @returns {ReturnType<import('polka').Middleware>}
		 */
		function handle(i) {
			if (i < handlers.length) {
				return handlers[i](req, res, () => handle(i + 1));
			} else {
				return next();
			}
		}

		return handle(0);
	};
}

const handler = sequence(
	/** @type {(import('sirv').RequestHandler | import('polka').Middleware)[]} */
	([serve(path.join(dir, 'client'), true), serve_prerendered(), ssr].filter(Boolean))
);

export { handler as h };
