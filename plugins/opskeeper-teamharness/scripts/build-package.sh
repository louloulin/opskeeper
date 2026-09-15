#!/usr/bin/env bash
set -euo pipefail

PLUGIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT_DIR="$(cd "$PLUGIN_DIR/../.." && pwd)"
cd "$PLUGIN_DIR"

VERSION="$(ruby -ryaml -e 'puts YAML.load_file("plugin.yaml").fetch("metadata").fetch("version")')"
BASE_PACKAGE="dist/opskeeper-teamharness-${VERSION}-plugin-manager.tar.gz"
DASHBOARD_PACKAGE="dist/opskeeper-teamharness-dashboard-${VERSION}.zip"

npm install --silent --prefix dashboard
npm run build --prefix dashboard
cp dashboard/dist/main.js "dashboard/dist/main-${VERSION}.js"
cp dashboard/dist/main.js.map "dashboard/dist/main-${VERSION}.js.map"
mkdir -p dist

OUT_DIR="$PLUGIN_DIR/dist" ruby adapters/qwenpaw/scripts/build-qwenpaw-plugin.rb plugin.yaml
rm -f "$BASE_PACKAGE"
rm -f "$DASHBOARD_PACKAGE"
ARCHIVE_HELPER="$ROOT_DIR/scripts/deterministic_archive.py"
python3 "$ARCHIVE_HELPER" tar-gz "$BASE_PACKAGE" \
    --source "$PLUGIN_DIR/plugin.yaml"=plugin.yaml \
    --source "$PLUGIN_DIR/prompts"=prompts \
    --source "$PLUGIN_DIR/skills"=skills \
    --source "$PLUGIN_DIR/mcp"=mcp \
    --source "$PLUGIN_DIR/adapters"=adapters \
    --source "$PLUGIN_DIR/scripts"=scripts \
    --source "$PLUGIN_DIR/loongsuite"=loongsuite \
    --source "$PLUGIN_DIR/examples"=examples \
    --source "$PLUGIN_DIR/README.md"=README.md \
    --source "$PLUGIN_DIR/CHANGELOG.md"=CHANGELOG.md \
    --source "$PLUGIN_DIR/dashboard"=dashboard \
    --source "$ROOT_DIR/LICENSE"=LICENSE \
    --source "$ROOT_DIR/NOTICE.md"=NOTICE.md

python3 "$ARCHIVE_HELPER" zip "$DASHBOARD_PACKAGE" \
    --source "$PLUGIN_DIR/dashboard/plugin.json"=plugin.json \
    --source "$PLUGIN_DIR/dashboard/dist/main.js"=dist/main.js \
    --source "$PLUGIN_DIR/dashboard/dist/main.js.map"=dist/main.js.map \
    --source "$PLUGIN_DIR/dashboard/dist/main-${VERSION}.js"="dist/main-${VERSION}.js" \
    --source "$PLUGIN_DIR/dashboard/dist/main-${VERSION}.js.map"="dist/main-${VERSION}.js.map"

printf '\nTeamHarness base package:\n'
ls -lh "$BASE_PACKAGE"
printf '\nSHA256:\n'
shasum -a 256 "$BASE_PACKAGE" "$DASHBOARD_PACKAGE" dist/opskeeper-teamharness-qwenpaw-"${VERSION}".zip
