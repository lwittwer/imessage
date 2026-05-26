#!/bin/bash
# Convenience wrapper installed at /usr/local/bin/imessage-setup so users
# can `docker exec -it bridge imessage-setup` instead of remembering
# `/entrypoint.sh setup`. Same behavior either way.
exec /entrypoint.sh setup "$@"
