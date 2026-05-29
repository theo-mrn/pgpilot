#!/bin/sh
# One WAL streaming agent per namespace.
# STREAM_CONFIGS: semicolon-separated entries, each pipe-separated:
#   HOST|PORT|USER|PASSWORD|WALG_S3_PREFIX|AWS_ACCESS_KEY_ID|AWS_SECRET_ACCESS_KEY|AWS_DEFAULT_REGION|ENDPOINT

if [ -z "$STREAM_CONFIGS" ]; then
  echo "STREAM_CONFIGS is required" >&2
  exit 1
fi

cleanup() {
  echo "Shutting down WAL streams..."
  kill 0
  wait
}
trap cleanup TERM INT

echo "Starting WAL streams..."

OLD_IFS="$IFS"
IFS=";"
for entry in $STREAM_CONFIGS; do
  IFS="|"
  set -- $entry
  HOST="$1" PORT="$2" USER="$3" PASSWORD="$4" \
  WALG_PREFIX="$5" ACCESS_KEY="$6" SECRET_KEY="$7" REGION="$8" ENDPOINT="$9"
  IFS=";"

  PORT="${PORT:-5432}"
  REGION="${REGION:-us-east-1}"

  echo "Streaming WALs from ${HOST}:${PORT} to ${WALG_PREFIX}..."

  (
    export PGHOST="$HOST" PGPORT="$PORT" PGUSER="$USER" PGPASSWORD="$PASSWORD"
    export WALG_S3_PREFIX="$WALG_PREFIX"
    export AWS_ACCESS_KEY_ID="$ACCESS_KEY"
    export AWS_SECRET_ACCESS_KEY="$SECRET_KEY"
    export AWS_DEFAULT_REGION="$REGION"
    export WALG_COMPRESSION_METHOD=lz4
    if [ -n "$ENDPOINT" ]; then
      export AWS_ENDPOINT="$ENDPOINT"
    fi

    WAL_DIR=$(mktemp -d)
    trap 'rm -rf "$WAL_DIR"' EXIT

    # Background watcher: push WAL segments to S3 every 30s.
    # Pushes both completed .gz and in-progress .partial segments.
    (
      while true; do
        sleep 30
        for f in "$WAL_DIR"/*.gz "$WAL_DIR"/*.partial; do
          [ -f "$f" ] || continue
          wal-g wal-push "$f" && rm -f "$f" || true
        done
      done
    ) &

    while true; do
      echo "[$HOST] Starting pg_receivewal..."
      pg_receivewal \
        --no-password \
        --directory="$WAL_DIR" \
        --synchronous || true

      # Push any remaining files after pg_receivewal exits.
      for f in "$WAL_DIR"/*.gz "$WAL_DIR"/*.partial; do
        [ -f "$f" ] || continue
        wal-g wal-push "$f" || true
        rm -f "$f"
      done

      echo "[$HOST] pg_receivewal exited, restarting in 5s..."
      sleep 5
    done
  ) &
done
IFS="$OLD_IFS"

wait
