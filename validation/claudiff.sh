#!/bin/bash
#
# This script compares data we've scraped to the data on claudielarouche.com.
#
set -euo pipefail
cd "$(dirname "$0")"

repo=claudielarouche/claudielarouche
file=assets/data/ottawa-drop-ins.csv
commit="$(set -eu; curl -sL "https://api.github.com/repos/$repo/commits?per_page=1&path=$file")"
updated="$(set -eu; jq -ncr --argjson x "$commit" '$x[0].commit.committer.date | gsub("T.+";"")')"
sha="$(set -eu; jq -ncr --argjson x "$commit" '$x[0].sha')"

prefix=clau.$updated
echo "$repo $file $sha $updated"
set -x

curl --fail -sL "https://github.com/$repo/raw/$sha/$file" |
python -c 'import csv, json, sys; print(json.dumps([dict(r) for r in csv.DictReader(sys.stdin)]))' |
jq -cr '
    .[] |
    with_entries(.key |= gsub(" ";"")) |
    with_entries(.value |= gsub("\\s+";" ")) |
    .Day |= ascii_downcase |
    .RegistrationRequired = if .RegistrationRequired == "Yes" then "true" else if .RegistrationRequired == "No" then "false" else "" end end |
    [.FacilityName, .ActivityType, .Time, .Day, .RegistrationRequired] |
    join(" | ")
' |
iconv -f utf-8 -t ascii//translit -c |
sort > $prefix.theirs.txt

curl --fail -sL "https://data.ottrec.ca/export/$updated.json" |
jq -cr --arg now "$updated" '
    INDEX(.facility[]; .url) as $f |
    .activity[] |
    .facilityName = $f[.facilityUrl].name |
    .rawActivity |= gsub(" *[*].*";"") |
    .time = .startTime + " - " + .endTime |
    .current = (.startDate == null or .startDate <= $now) and ($now <= .endDate or .endDate == null) |
    select(.current) |
    [.facilityName, .rawActivity, .time, .weekday, .reservationRequired] |
    join(" | ")
' |
iconv -f utf-8 -t ascii//translit -c |
sort > $prefix.ours.txt

git diff --no-index $prefix.ours.txt $prefix.theirs.txt > $prefix.diff
