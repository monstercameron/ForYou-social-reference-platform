# Demo media

Public sample files that seeded demo posts reference as external media slots. GitHub Pages serves them over HTTPS, so a client fetches them the way it fetches any media: after consent, from a public address, verifying the SHA-256 the post signed.

| File | Source |
| --- | --- |
| `landscape-1.png`, `landscape-2.png`, `landscape-3.png` | Original project illustrations |
| `flower.mp4` | MDN interactive example, CC0: https://interactive-examples.mdn.mozilla.net/media/cc0-videos/flower.mp4 |

Changing a file changes its hash, and posts that reference the old bytes then show a failed integrity check. Add new files instead of replacing these.
