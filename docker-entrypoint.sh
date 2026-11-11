#!/bin/sh
set -e

# Route commands to the appropriate Resumeee binary
if [ "$1" = "server" ] || [ "$1" = "resumeee-server" ]; then
    shift
    exec /usr/local/bin/resumeee-server "$@"
elif [ "$1" = "mcp" ] || [ "$1" = "resumeee-mcp" ]; then
    shift
    exec /usr/local/bin/resumeee-mcp "$@"
elif [ "${1#-}" != "$1" ]; then
    exec /usr/local/bin/resumeee-server "$@"
elif [ -x "$1" ]; then
    exec "$@"
else
    exec /usr/local/bin/resumeee-server "$@"
fi
