AI Disclosure

- Add an explicit co-author trailer to every commit as the last line, using the AI’s name and the maker’s noreply domain, e.g.: `Co-Authored-By: Codex CLI Agent <noreply@openai.com>`
- Use the exact format `Co-Authored-By: <name> <noreply@maker-domain>`.
- Never use `git add .` or `git commit -A`; stage files explicitly.
- On a git `index.lock` error, rerun the same command.
- If a git operation fails with a permission error, rerun it with escalated permissions.
