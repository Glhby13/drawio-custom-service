#!/bin/bash
# DISPOSABLE SPIKE — NOT PRODUCTION CODE: container hardening matrix for jgraph/drawio:32.6.1
IMG=jgraph/drawio:32.6.1
run() { # name platform port flags...
  local name=$1 plat=$2 port=$3; shift 3
  docker rm -f hd-$name >/dev/null 2>&1
  docker run -d --name hd-$name --platform linux/$plat -p 127.0.0.1:$port:8080 -e DRAWIO_SERVER_URL=http://localhost:8790/editor/ -e DRAWIO_BASE_URL=http://localhost:8790/editor "$@" $IMG >/dev/null 2>&1
  local code="down" path=/editor/
  for i in $(seq 1 90); do
    c=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$port/editor/ 2>/dev/null); [ "$c" = 200 ] && { code=200; break; }
    c=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$port/ 2>/dev/null); [ "$c" = 200 ] && { code=200; path=/; break; }
    sleep 2
  done
  local warn=$(docker logs hd-$name 2>&1 | grep -E "WARNING|Permission denied|denied|Read-only|Exception" | head -3 | tr '\n' ' ' | cut -c1-260)
  local base=$(docker exec hd-$name sh -c 'grep -c "localhost:8790/editor" /usr/local/tomcat/webapps/draw/js/PreConfig.js' 2>/dev/null)
  local who=$(docker exec hd-$name id 2>/dev/null | cut -c1-60)
  printf '%-24s %-6s http=%s servedAt=%s preconfigApplied=%s id=[%s] warn=[%s]\n' "$name" "$plat" "$code" "$path" "${base:-n/a}" "$who" "$warn"
  docker rm -f hd-$name >/dev/null 2>&1
}
run default-arm64      arm64 8101
run uid1234-gid0-arm64 arm64 8102 --user 1234:0
run uid1234-gid1234    arm64 8103 --user 1234:1234
run readonly-tmpfs     arm64 8104 --read-only --tmpfs /tmp --tmpfs /usr/local/tomcat/logs --tmpfs /usr/local/tomcat/temp --tmpfs /usr/local/tomcat/work
run capdrop-nonewpriv  arm64 8105 --cap-drop ALL --security-opt no-new-privileges --user 1234:0
run uid1234-gid0-amd64 amd64 8106 --user 1234:0
run capdrop-amd64      amd64 8107 --cap-drop ALL --security-opt no-new-privileges --user 1234:0
