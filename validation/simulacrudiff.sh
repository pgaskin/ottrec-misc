#!/bin/bash
#
# This script compares data we've scraped to the data on simulacrumus's LLM-based scraper.
#
set -euo pipefail
cd "$(dirname "$0")"

updated=$(set -x; curl -sL https://api.github.com/repos/simulacrumus/ottawa-drop-in-activity-scraper/commits | jq -cr '.[].commit.message' | grep -Pom1 '(?<=Update schedules and caches \()[0-9-]+')
prefix=simulacru.$updated

repo=simulacrumus/ottawa-drop-in-activity-scraper
file=schedules.json
commit="$(set -eu; curl -sL "https://api.github.com/repos/$repo/commits?per_page=1&path=$file")"
updated="$(set -eu; jq -ncr --argjson x "$commit" '$x[0].commit.committer.date | gsub("T.+";"")')"
sha="$(set -eu; jq -ncr --argjson x "$commit" '$x[0].sha')"

prefix=clau.$updated
echo "$repo $file $sha $updated"
set -x

curl --fail -sL "https://github.com/$repo/raw/$sha/$file" |
jq -cr '
    .[] |
    .dayOfWeek = ["", "monday", "tuesday","wednesday","thursday","friday","saturday","sunday"][.dayOfWeek] |
    [.facility, .activity, .startTime, .endTime, .dayOfWeek, .periodStartDate, .periodEndDate] |
    join(" | ")
' |
iconv -f utf-8 -t ascii//translit -c |
sort > $prefix.theirs.txt

curl --fail -sL "https://data.ottrec.ca/export/$updated.json" |
jq -cr '
    INDEX(.facility[]; .url) as $f |
    .activity[] |
    .facilityName = $f[.facilityUrl].name |
    .rawActivity |= gsub(" *[*].*";"") |
    [.facilityName, .rawActivity, .startTime, .endTime, .weekday, .startDate, .endDate] |
    join(" | ")
' |
iconv -f utf-8 -t ascii//translit -c |
sort > $prefix.ours.txt

git diff --no-index $prefix.ours.txt $prefix.theirs.txt > simulacru.$updated.diff
