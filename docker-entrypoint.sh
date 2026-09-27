#!/bin/sh
set -e

# Se nenhum argumento for passado, o padrão é iniciar a api
if [ $# -eq 0 ]; then
    set -- api
fi

case "$1" in
  api)
    shift
    exec /app/bin/api "$@"
    ;;
  ingester)
    shift
    exec /app/bin/ingester "$@"
    ;;
  dispatcher)
    shift
    exec /app/bin/dispatcher "$@"
    ;;
  *)
    # Se passar um caminho de binário ou comando shell direto
    if [ -x "/app/bin/$1" ]; then
      cmd="/app/bin/$1"
      shift
      exec "$cmd" "$@"
    else
      exec "$@"
    fi
    ;;
esac
