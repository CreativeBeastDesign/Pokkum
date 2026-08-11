# Rule: Serena MCP Memory Integration

- **Startup Requirement**: On every new task, query Serena MCP (`read_memory("core")`) to inspect the project memory graph.
- **Memory References**: Follow memory references formatted as `mem:<name>` (e.g. `mem:conventions`, `mem:tech_stack`, `mem:telemetry`, `mem:task_completion`).
- **Memory Maintenance**: When making non-trivial code modifications, architectural updates, CLI flag additions, or convention changes, update Serena's memories using `write_memory` or `edit_memory`.
- **Symbolic Tools First**: Prefer Serena's symbolic search and refactoring tools (`get_symbols_overview`, `find_symbol`, `find_referencing_symbols`, `replace_symbol_body`, `rename_symbol`) over reading full files or raw file replacement.
