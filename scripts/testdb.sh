#!/bin/sh
# Starts a MySQL-compatible server in Docker and creates what the tests expect.
#
#   scripts/testdb.sh IMAGE [PORT]      e.g. scripts/testdb.sh mysql:5.7 33057
#   GOMYSQL_ADDR=127.0.0.1:33057 go test ./...
#   docker rm -f gomysql-test-33057
#
# Database gomysql; users (password = name):
#   b    mysql_native_password where the server has it, else the default plugin
#   c2   caching_sha2_password (MySQL/Percona 8.0+ only)
#   lim  like b, MAX_USER_CONNECTIONS 7
set -eu

image=${1:?usage: $0 IMAGE [PORT]}
port=${2:-33306}
name=gomysql-test-$port

flags=
platform=
native="WITH mysql_native_password"
sha2=yes
case $image in
mariadb:* | */mariadb:*) native=; sha2= ;;
mysql:5.*) sha2= platform=--platform=linux/amd64 ;; # no arm64 image
*:5.*) sha2= ;;
*:8.4* | *:8.4) flags=--mysql-native-password=ON ;;
*:9.* | *:9 | *:latest | *:innovation) native= ;;
esac

docker rm -f "$name" >/dev/null 2>&1 || true
# shellcheck disable=SC2086 # $platform and $flags are empty or one word
docker run -d $platform --name "$name" -p "127.0.0.1:$port:3306" \
	-e MYSQL_ROOT_PASSWORD=root -e MARIADB_ROOT_PASSWORD=root \
	"$image" $flags >/dev/null

client=mysql
docker exec "$name" sh -c 'command -v mysql' >/dev/null 2>&1 || client=mariadb

# The entrypoint initialises with a socket-only server, then restarts with TCP.
i=0
until docker exec -e MYSQL_PWD=root "$name" $client -h127.0.0.1 -uroot -e 'SELECT 1' >/dev/null 2>&1; do
	i=$((i + 1))
	if [ $i -gt 180 ]; then
		docker logs --tail 50 "$name" >&2
		echo "$name did not start" >&2
		exit 1
	fi
	sleep 1
done

{
	echo "CREATE DATABASE gomysql;"
	echo "CREATE USER 'b'@'%' IDENTIFIED $native BY 'b';"
	echo "CREATE USER 'lim'@'%' IDENTIFIED $native BY 'lim' WITH MAX_USER_CONNECTIONS 7;"
	echo "GRANT ALL ON gomysql.* TO 'b'@'%', 'lim'@'%';"
	if [ -n "$sha2" ]; then
		echo "CREATE USER 'c2'@'%' IDENTIFIED WITH caching_sha2_password BY 'c2';"
		echo "GRANT ALL ON gomysql.* TO 'c2'@'%';"
	fi
} | docker exec -i -e MYSQL_PWD=root "$name" $client -h127.0.0.1 -uroot

docker exec -e MYSQL_PWD=root "$name" $client -h127.0.0.1 -uroot -N -e 'SELECT VERSION()'
echo "GOMYSQL_ADDR=127.0.0.1:$port"
