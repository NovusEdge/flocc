# flocc

flocc runs Claude Code sessions on Linear issues. Label an issue and assign it to yourself, and a host you run picks it up, works on it in its own git worktree, and opens a draft PR. A Go command on your desktop (`flocc`) sets hosts up and shows the fleet. A Python service on each host does the work.

It is built for one owner running a few hosts, not for a team sharing one service.

The documentation is a book in [`docs/book/`](docs/book/README.md), published with GitBook. It covers setup, keys, the tailnet, every command, cardinal, troubleshooting and the architecture.

To start, run `./setup`, build the desktop command with `go install ./installer/cmd/flocc`, then follow [Getting started](docs/book/getting-started.md).
