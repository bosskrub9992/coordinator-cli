#!/usr/bin/env bash
set -euo pipefail

REPO_DIR=$(cd "$(dirname "$0")/.." && pwd)
MODEL=${SMOKE_MODEL:-claude-sonnet-5-5}
EFFORT=${SMOKE_EFFORT:-low}
TIMEOUT=${SMOKE_TIMEOUT:-600}

ROOT=$(cd "$(mktemp -d "${TMPDIR:-/tmp}/coord-smoke.XXXXXX")" && pwd -P)
echo "smoke root: $ROOT"

mkdir -p "$ROOT/bin" "$ROOT/home" "$ROOT/launch" "$ROOT/nohooks"
(cd "$REPO_DIR" && go build -o "$ROOT/bin/coord" ./cmd/coord)

export PATH="$ROOT/bin:$PATH"
export COORD_HOME="$ROOT/home"
export TREEHOUSE_ROOT="$ROOT/pool"
unset COORD_TOKEN COORD_ROLE COORD_TASK

ORIGIN="$ROOT/origin.git"
MAIN="$ROOT/launch/smoke-repo"
git init -q --bare "$ORIGIN"
git init -q -b main "$MAIN"
git -C "$MAIN" config core.hooksPath "$ROOT/nohooks"
printf '# smoke repo\n' > "$MAIN/README.md"
printf '@AGENTS.md\n' > "$MAIN/CLAUDE.md"
printf 'Smoke repo rule: keep commits to one file.\n' > "$MAIN/AGENTS.md"
printf 'max_trees = 2\n' > "$MAIN/treehouse.toml"
git -C "$MAIN" add .
git -C "$MAIN" commit -q -m init
git -C "$MAIN" remote add origin "file://localhost$ORIGIN"
git -C "$MAIN" push -q origin main
MAIN_HEAD=$(git -C "$MAIN" rev-parse HEAD)

cat > "$COORD_HOME/config.json" <<EOF
{"classes": {"ship": {"harness": "claude", "model": "$MODEL", "effort": "$EFFORT"}}}
EOF

cd "$ROOT/launch"
coord project add "$MAIN" --name smoke-repo
ID=$(coord task new --project smoke-repo --class ship --title "Smoke hello" --skip-plan --brief - <<'EOF'
Smoke test of coordinator-cli. Keep it minimal and do not explore.

1. In your worktree (listed under "Where to work"), create a file named hello.txt whose whole content is the line: hi
2. Commit it on the current branch with the message "smoke: hello".
3. Push the branch with plain `git push -u origin HEAD`, run from the worktree.
4. Do not open an MR or PR.
5. Finish with `coord report --status done --file -` and a one-line Report naming the commit hash.
EOF
)
echo "task: $ID"
coord spawn "$ID"

TASK_DIR="$COORD_HOME/tasks/$ID"
state() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["state"])' "$TASK_DIR/state.json"; }
supervisor_live() {
  local pid
  pid=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("supervisor_pid", 0))' "$TASK_DIR/worker.json" 2>/dev/null || echo 0)
  if [ "$pid" != 0 ] && kill -0 "$pid" 2>/dev/null; then echo yes; else echo no; fi
}

deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  st=$(state)
  case "$st" in
    reported|waiting-review|failed|blocked|needs-decision)
      if [ "$(supervisor_live)" = no ] || [ "$st" = blocked ] || [ "$st" = needs-decision ]; then break; fi;;
  esac
  if [ "$(date +%s)" -gt "$deadline" ]; then echo "timeout in state $st"; break; fi
  sleep 2
done

echo
echo "== coord watch $ID (tail)"
coord watch "$ID" | tail -40
echo
echo "== coord show $ID"
coord show "$ID"
REFUSED=$(python3 -c 'import subprocess, sys; r = subprocess.run(["coord", "stop", sys.argv[1]], stdin=subprocess.DEVNULL, capture_output=True, text=True, start_new_session=True); print(r.returncode, r.stderr)' "$ID")
echo
echo "== events"
python3 - "$TASK_DIR/events.jsonl" <<'EOF'
import json, sys
for line in open(sys.argv[1]):
    e = json.loads(line)
    print(e["seq"], e["type"], (e.get("from","") + ">" + e.get("to","")) if e.get("to") else "", (e.get("text") or "")[:160])
EOF

fail=0
check() { if eval "$2"; then echo "PASS $1"; else echo "FAIL $1"; fail=1; fi; }
BRANCH="coord/$ID"
FINAL=$(state)
echo
check "state is reported (ship, no MR)" '[ "$FINAL" = reported ]'
check "report.md written" '[ -s "$TASK_DIR/report.md" ]'
check "commit on $BRANCH in the bare remote" 'git --git-dir "$ORIGIN" rev-parse --verify -q "refs/heads/$BRANCH" >/dev/null'
check "remote branch has hello.txt = hi" '[ "$(git --git-dir "$ORIGIN" show "$BRANCH:hello.txt" 2>/dev/null)" = hi ]'
check "main checkout clean" '[ -z "$(git -C "$MAIN" status --porcelain)" ]'
check "main checkout HEAD unchanged on main" '[ "$(git -C "$MAIN" rev-parse HEAD)" = "$MAIN_HEAD" ] && [ "$(git -C "$MAIN" rev-parse --abbrev-ref HEAD)" = main ]'
check "no hello.txt in main checkout" '[ ! -e "$MAIN/hello.txt" ]'
check "worker exited" '[ "$(supervisor_live)" = no ]'
check "Worker ran in auto permission mode" 'grep -q "\"permissionMode\":\"auto\"" "$TASK_DIR/worker.log"'
check "no permission requests or MCP config" '[ ! -e "$TASK_DIR/permissions" ] && [ ! -e "$TASK_DIR/mcp.json" ]'
check "coord show prints the Report" '[ "$(coord show "$ID" --report)" = "$(cat "$TASK_DIR/report.md")" ]'
check "a detached process with no terminal is refused" 'grep -q "changes the Fleet" <<<"$REFUSED"'
echo
echo "report: $(cat "$TASK_DIR/report.md" 2>/dev/null)"
echo "remote: $(git --git-dir "$ORIGIN" log --oneline -1 "$BRANCH" 2>/dev/null)"
echo "artifacts kept in $ROOT"
exit $fail
