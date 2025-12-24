#!/bin/bash
# A helper script to run multiple bots on same IP at once, with specific bot binary
#
# Environment variables:
#   USERNAMES      - space-separated list of usernames (required)
#   CMD_PREFIX     - command template with <USERNAME> placeholder (required)
#   STARTUP_DELAY  - delay in seconds between each bot startup (default: 10)
#
# Example:
#   USERNAMES="Bot1 Bot2 Bot3" CMD_PREFIX="./botbinary -s 127.0.0.1 -u <USERNAME>" STARTUP_DELAY=5 ./multi.sh

if [[ -z "$USERNAMES" ]]; then
    echo "Error: USERNAMES environment variable is required"
    echo "Example: USERNAMES=\"Bot1 Bot2 Bot3\""
    exit 1
fi

if [[ -z "$CMD_PREFIX" ]]; then
    echo "Error: CMD_PREFIX environment variable is required"
    echo "Example: CMD_PREFIX=\"./botbinary -s 127.0.0.1 -u <USERNAME>\""
    exit 1
fi

read -ra USERNAMES_ARR <<< "$USERNAMES"

STARTUP_DELAY="${STARTUP_DELAY:-10}"

PIDS=()
cleanup() {
    echo ""
    echo "Stopping all processes..."
    for pid in "${PIDS[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null
        fi
    done
    wait 2>/dev/null
    echo "All processes stopped."
    exit 0
}

trap cleanup SIGINT SIGTERM EXIT

first=true
for username in "${USERNAMES_ARR[@]}"; do
    if [[ "$first" == true ]]; then
        first=false
    else
        echo "Waiting ${STARTUP_DELAY}s before next bot..."
        sleep "$STARTUP_DELAY"
    fi
    cmd="${CMD_PREFIX//<USERNAME>/$username}"
    echo "Starting: $cmd"
    eval "$cmd" &
    PIDS+=($!)
done

echo ""
echo "Started ${#PIDS[@]} processes. Press Ctrl+C to stop all."
echo ""

wait
