#!/bin/bash
# A helper script to run multiple bots on same IP at once, with specific bot binary

# >>> configure these:
USERNAMES=(
    "Bot1"
    "Bot2"
    "Bot3"
)
CMD_PREFIX="./botbinary -s 127.0.0.1 -u <USERNAME>"

# <<< end of configuration section

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

for username in "${USERNAMES[@]}"; do
    cmd="${CMD_PREFIX//<USERNAME>/$username}"
    echo "Starting: $cmd"
    eval "$cmd" &
    PIDS+=($!)
done

echo ""
echo "Started ${#PIDS[@]} processes. Press Ctrl+C to stop all."
echo ""

wait
