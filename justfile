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

# Build the desktop flocc command into build/
[group('build')]
build:
    mkdir -p build
    go build -o build/flocc ./installer/cmd/flocc

# Remove build/
[group('build')]
clean:
    rm -rf build

# Set up or upgrade a host without a terminal (--answers FILE sends the answers), or --interactive
[group('host')]
ship target *flags: build
    build/flocc ship {{flags}} {{target}}

# Log the host in to Claude, accept Remote Control and start its timer (--email picks the account)
[group('host')]
login target *flags: build
    build/flocc login {{flags}} {{target}}

# Run a raw command on the host as flocc, with the service PATH
[group('host')]
[positional-arguments]
host name *cmd: build
    t=$1; shift; build/flocc host run -H "$t" -- "$@"

# Run flocc doctor on the host
[group('host')]
doctor name: build
    build/flocc -H {{name}} doctor

# Show tmux sessions, the last run.log lines and the timer on the host
[group('host')]
status name: build
    build/flocc -H {{name}} status

# Show every host's loop, sessions, usage and disk in one table
[group('host')]
fleet-status *flags: build
    build/flocc status -H all {{flags}}

# Follow run.log, or the journal on a systemd host
[group('host')]
logs name: build
    build/flocc -H {{name}} logs -f

# Attach read-only to a tmux session on the host
[group('host')]
attach name session: build
    build/flocc -H {{name}} attach {{session}}

# Run one tick by hand on the host
[group('host')]
tick name: build
    build/flocc -H {{name}} tick

# Release a blocked or timed-out issue's claim on the host
[group('host')]
[positional-arguments]
release name key *flags: build
    t=$1; shift; build/flocc -H "$t" release "$@"

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
