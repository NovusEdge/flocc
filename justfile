# Must match state_root in the host's config.toml. Only status/logs on non-systemd
# (loop) hosts read it; systemd hosts use journald.
runlog := "~/.local/state/linear-agents/run.log"

[private]
default:
    @just --list --unsorted

# Format Python and Go sources in place
[group('dev')]
fmt:
    uv run ruff check --select I --fix
    uv run ruff format
    gofmt -w deploy installer

# Lint Python, the setup script and Go without changing files
[group('dev')]
lint:
    uv run ruff format --check
    uv run ruff check
    uv run pyrefly check
    shellcheck setup
    test -z "$(gofmt -l deploy installer)"
    go vet ./...

# Run the Python and Go test suites
[group('dev')]
test: test-py test-go

# Run the Python tests
[group('dev')]
test-py:
    uv run pytest -q

# Run the Go tests
[group('dev')]
test-go:
    go test ./...

# Lint, then test; what CI and pre-merge run
[group('dev')]
check: lint test

# Build la-setup for this machine and for linux amd64/arm64 hosts into build/
[group('build')]
build-setup:
    mkdir -p build
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/la-setup-linux-amd64 ./installer
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/la-setup-linux-arm64 ./installer
    go build -o build/la-setup ./installer

[private]
build-desktop:
    mkdir -p build
    go build -o build/la-setup ./installer

# Remove build/
[group('build')]
clean:
    rm -rf build

# Set up or upgrade a host: an ssh destination (a Tailscale name) or daytona:<sandbox>
[group('host')]
ship target *flags: build-setup
    build/la-setup ship {{flags}} {{target}}

# Run any command on the host as linear-agents, with the service PATH
[group('host')]
[positional-arguments]
host target *cmd: build-desktop
    build/la-setup host "$@"

# Run linear-agents doctor on the host
[group('host')]
doctor target: build-desktop
    build/la-setup host {{target}} linear-agents doctor

# Show tmux sessions, the last run.log lines and the timer on the host
[group('host')]
status target: build-desktop
    build/la-setup host {{target}} 'tmux -L linear-agents ls; tail -n 10 {{runlog}}; [ -d /run/systemd/system ] && XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user list-timers linear-agents.timer; true'

# Show every host's loop, sessions, usage and disk in one table
[group('host')]
fleet-status *flags: build-desktop
    build/la-setup fleet {{flags}}

# Follow run.log, or the journal on a systemd host
[group('host')]
logs target: build-desktop
    build/la-setup host {{target}} 'if [ -d /run/systemd/system ]; then journalctl --user -u linear-agents -f; else tail -f {{runlog}}; fi'

# Attach read-only to a tmux session on the host
[group('host')]
attach target session: build-desktop
    build/la-setup host {{target}} tmux -L linear-agents attach -r -t {{session}}

# Run one tick by hand on the host
[group('host')]
tick target: build-desktop
    build/la-setup host {{target}} linear-agents tick

# Release a blocked or timed-out issue's claim on the host
[group('host')]
[positional-arguments]
release target key *flags: build-desktop
    t=$1; shift; build/la-setup host "$t" linear-agents release "$@"

# Create a Daytona sandbox that never auto-stops
[group('daytona')]
daytona-up name:
    daytona create --snapshot daytona-medium --name {{name}} --auto-stop 0

# Stop a Daytona sandbox
[group('daytona')]
daytona-stop name:
    daytona stop {{name}}

# Start a Daytona sandbox, then ship to it
[group('daytona')]
daytona-start name:
    daytona start {{name}}
    just ship daytona:{{name}}

# List Daytona sandboxes
[group('daytona')]
daytona-list:
    daytona list
