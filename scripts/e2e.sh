#!/usr/bin/env bash
# End-to-end smoke test against a running stack (compose up first).
# Proves the full loop: api -> redis -> queue -> worker -> engine -> redis
# -> archive. Exits non-zero on any failure.
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"

json() { python3 -c "import sys, json; print(json.load(sys.stdin)$1)"; }

# A cookie jar, because a game belongs to whoever created it and the session
# cookie is how the server knows that. Without one, every request here is a
# different anonymous visitor and the second one cannot touch the game the
# first one made. A browser has always carried these; this script did not, and
# it only passed while the server let anybody move in any bot game.
jar=$(mktemp)
trap 'rm -f "$jar"' EXIT
play() { curl -sf -b "$jar" -c "$jar" "$@"; }

wait_for_ply() {
  local id=$1 want=$2
  for _ in $(seq 1 40); do
    ply=$(curl -sf "$BASE/api/games/$id" | json "['moves'].__len__()")
    if [ "$ply" -ge "$want" ]; then return 0; fi
    sleep 0.5
  done
  echo "timed out waiting for ply $want on game $id" >&2
  return 1
}

echo "1. health check"
curl -sf "$BASE/healthz" > /dev/null

echo "2. play black: engine must open"
id=$(play -X POST "$BASE/api/games" -H 'Content-Type: application/json' \
  -d '{"color":"black"}' | json "['id']")
wait_for_ply "$id" 1
echo "   engine opened in game $id"

echo "3. play white: 1. e4, engine must reply"
id=$(play -X POST "$BASE/api/games" -H 'Content-Type: application/json' \
  -d '{"color":"white"}' | json "['id']")
play -X POST "$BASE/api/games/$id/moves" -H 'Content-Type: application/json' \
  -d '{"uci":"e2e4"}' > /dev/null
wait_for_ply "$id" 2
reply=$(curl -sf "$BASE/api/games/$id" | json "['moves'][1]")
echo "   engine replied $reply"

echo "4. illegal and out-of-turn moves are rejected"
code=$(curl -s -b "$jar" -c "$jar" -o /dev/null -w '%{http_code}' \
  -X POST "$BASE/api/games/$id/moves" \
  -H 'Content-Type: application/json' -d '{"uci":"e2e4"}')
[ "$code" = "400" ] || { echo "   expected 400, got $code" >&2; exit 1; }

echo "5. resignation archives the game"
play -X POST "$BASE/api/games/$id/resign" > /dev/null
sleep 1
total=$(curl -sf "$BASE/api/stats" | json "['total']")
[ "$total" -ge 1 ] || { echo "   stats empty after resign" >&2; exit 1; }
echo "   stats: $(curl -sf "$BASE/api/stats")"

echo "6. accounts: signup, session, logout"
jar=$(mktemp)
user="e2e_$RANDOM$RANDOM"

# Signed out, /me reports no user rather than 401. Anonymous play is supported,
# so the frontend asks this on every load and must not treat it as an error.
who=$(curl -sf "$BASE/api/auth/me" | json "['user']")
[ "$who" = "None" ] || { echo "   expected no user before signup, got $who" >&2; exit 1; }

curl -sf -c "$jar" -X POST "$BASE/api/auth/signup" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$user\",\"password\":\"correct horse battery\"}" > /dev/null
name=$(curl -sf -b "$jar" "$BASE/api/auth/me" | json "['user']['username']")
[ "$name" = "$user" ] || { echo "   session did not resolve, got $name" >&2; exit 1; }
echo "   signed up and session resolves as $name"

# Same username again must lose to the unique index, not create a second row.
# tr rather than ${user^^}, which needs bash 4 and macOS ships 3.2.
upper=$(printf '%s' "$user" | tr '[:lower:]' '[:upper:]')
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/auth/signup" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$upper\",\"password\":\"correct horse battery\"}")
[ "$code" = "409" ] || { echo "   expected 409 on duplicate username, got $code" >&2; exit 1; }
echo "   duplicate username rejected case insensitively"

code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$user\",\"password\":\"wrong password\"}")
[ "$code" = "401" ] || { echo "   expected 401 on bad password, got $code" >&2; exit 1; }

curl -sf -b "$jar" -c "$jar" -X POST "$BASE/api/auth/logout" > /dev/null
who=$(curl -sf -b "$jar" "$BASE/api/auth/me" | json "['user']")
[ "$who" = "None" ] || { echo "   still signed in after logout" >&2; exit 1; }
echo "   logout revoked the session"
rm -f "$jar"

echo "7. a signed-in game is attached to the account and rated"
jar2=$(mktemp)
ruser="e2e_r$RANDOM$RANDOM"
curl -sf -c "$jar2" -X POST "$BASE/api/auth/signup" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ruser\",\"password\":\"correct horse battery\"}" > /dev/null

before=$(curl -sf -b "$jar2" "$BASE/api/me/profile" | json "['rating']")
gid=$(curl -sf -b "$jar2" -X POST "$BASE/api/games" -H 'Content-Type: application/json' \
  -d '{"color":"white"}' | json "['id']")
curl -sf -b "$jar2" -X POST "$BASE/api/games/$gid/resign" > /dev/null
sleep 1

after=$(curl -sf -b "$jar2" "$BASE/api/me/profile" | json "['rating']")
python3 -c "import sys; sys.exit(0 if $after < $before else 1)" \
  || { echo "   rating did not fall after a loss: $before -> $after" >&2; exit 1; }
echo "   rating moved $before -> $after after losing"

games=$(curl -sf -b "$jar2" "$BASE/api/me/games" | json "['games'].__len__()")
[ "$games" = "1" ] || { echo "   expected 1 game in history, got $games" >&2; exit 1; }
echo "   game appears in history"

# The api and the worker both archive a finished game. Rating a game twice is
# the bug the ON CONFLICT gate exists to stop, so re-resigning must not move it.
curl -s -o /dev/null -X POST "$BASE/api/games/$gid/resign" || true
sleep 1
again=$(curl -sf -b "$jar2" "$BASE/api/me/profile" | json "['rating']")
[ "$again" = "$after" ] || { echo "   rating changed on replay: $after -> $again" >&2; exit 1; }
echo "   replayed archive did not double-count"

# A separate visitor with their own session, which is what "anonymous" means
# here: not signed in, but still identifiable enough to own the game they just
# made. The point of the check is that this game does not land in the
# signed-in account above.
ajar=$(mktemp)
anon=$(curl -sf -c "$ajar" -b "$ajar" -X POST "$BASE/api/games" \
  -H 'Content-Type: application/json' -d '{"color":"white"}' | json "['id']")
curl -sf -c "$ajar" -b "$ajar" -X POST "$BASE/api/games/$anon/resign" > /dev/null
rm -f "$ajar"
still=$(curl -sf -b "$jar2" "$BASE/api/me/games" | json "['games'].__len__()")
[ "$still" = "1" ] || { echo "   an anonymous game leaked into the account" >&2; exit 1; }
echo "   anonymous games stay unattached"
rm -f "$jar2"

echo "8. a guest can play and rate without signing up, then keep it"
gjar=$(mktemp)

# No account, no prompt. The first game mints a guest and counts.
ggid=$(curl -sf -c "$gjar" -b "$gjar" -X POST "$BASE/api/games" \
  -H 'Content-Type: application/json' -d '{"color":"white"}' | json "['id']")
curl -sf -b "$gjar" -X POST "$BASE/api/games/$ggid/resign" > /dev/null
sleep 1

guest=$(curl -sf -b "$gjar" "$BASE/api/auth/me" | json "['user']['guest']")
[ "$guest" = "True" ] || { echo "   expected a guest identity, got $guest" >&2; exit 1; }
grating=$(curl -sf -b "$gjar" "$BASE/api/me/profile" | json "['rating']")
python3 -c "import sys; sys.exit(0 if $grating < 1500 else 1)" \
  || { echo "   guest game was not rated: $grating" >&2; exit 1; }
echo "   guest played and got rated to $grating"

# Signing up keeps the rating and the history, because the guest row is the
# account rather than something to migrate from.
guser="e2e_g$RANDOM$RANDOM"
curl -sf -b "$gjar" -c "$gjar" -X POST "$BASE/api/auth/signup" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$guser\",\"password\":\"correct horse battery\"}" > /dev/null
kept=$(curl -sf -b "$gjar" "$BASE/api/me/profile" | json "['rating']")
[ "$kept" = "$grating" ] || { echo "   rating lost on signup: $grating -> $kept" >&2; exit 1; }
khist=$(curl -sf -b "$gjar" "$BASE/api/me/games" | json "['games'].__len__()")
[ "$khist" = "1" ] || { echo "   history lost on signup, got $khist games" >&2; exit 1; }
nowguest=$(curl -sf -b "$gjar" "$BASE/api/auth/me" | json "['user']['guest']")
[ "$nowguest" = "False" ] || { echo "   still a guest after signing up" >&2; exit 1; }
echo "   signup kept the rating and history in place"
rm -f "$gjar"

echo "9. reads do not mint accounts, and guests do not get their own bucket"
before=$(curl -sf "$BASE/api/status" | json "['games']['total']" 2>/dev/null || echo 0)

# A GET with no cookie must answer without creating anything. Looping over it
# used to be an unauthenticated way to fill the users table.
for _ in 1 2 3 4 5; do curl -sf -o /dev/null "$BASE/api/me/profile"; done
noacct=$(curl -sf -D /tmp/h.txt -o /dev/null "$BASE/api/me/profile"; grep -ci "set-cookie" /tmp/h.txt || true)
[ "$noacct" = "0" ] || { echo "   a read handed out a session cookie" >&2; exit 1; }
prov=$(curl -sf "$BASE/api/me/profile" | json "['provisional']")
[ "$prov" = "True" ] || { echo "   expected a provisional default profile, got $prov" >&2; exit 1; }
echo "   reads answer without creating an account"

hist=$(curl -sf "$BASE/api/me/games" | json "['games'].__len__()")
[ "$hist" = "0" ] || { echo "   expected empty history for no identity, got $hist" >&2; exit 1; }
echo "   history is empty rather than an error"

# Signing up must not leave the guest token usable, or whoever planted that
# cookie keeps access to the account it became.
rjar=$(mktemp); rold=$(mktemp)
curl -sf -c "$rjar" -X POST "$BASE/api/games" -H 'Content-Type: application/json' \
  -d '{"color":"white"}' > /dev/null
oldtok=$(grep bn_session "$rjar" | awk '{print $7}')
ruser="e2e_s$RANDOM$RANDOM"
curl -sf -b "$rjar" -c "$rjar" -X POST "$BASE/api/auth/signup" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ruser\",\"password\":\"correct horse battery\"}" > /dev/null
whoold=$(curl -sf -H "Cookie: bn_session=$oldtok" "$BASE/api/auth/me" | json "['user']")
[ "$whoold" = "None" ] || { echo "   the pre-signup token still resolves: $whoold" >&2; exit 1; }
echo "   signup rotated the session and killed the old token"
rm -f "$rjar" "$rold" /tmp/h.txt

echo "e2e ok"
