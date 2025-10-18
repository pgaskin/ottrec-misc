#!/bin/bash
#
# This script compares data we've scraped to the data on claudielarouche.com.
#
set -euxo pipefail
cd "$(dirname "$0")"

updated=$(set -x; curl -sL https://claudielarouche.com/projects/ottawa-drop-ins/ | grep -Pom1 '(?<=Data last updated: )[0-9-]+')
prefix=clau.$updated

curl --fail -sL https://claudielarouche.com/assets/data/ottawa-drop-ins.csv |
python -c 'import csv, json, sys; print(json.dumps([dict(r) for r in csv.DictReader(sys.stdin)]))' |
jq -cr '
    .[] |
    with_entries(.key |= gsub(" ";"")) |
    with_entries(.value |= gsub("\\s+";" ")) |
    .Day |= ascii_downcase |
    .RegistrationRequired = if .RegistrationRequired = "Yes" then "true" else if .RegistrationRequired = "No" then "false" else "" end end |
    [.FacilityName, .ActivityType, .Time, .Day, .RegistrationRequired] |
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
    .time = .startTime + " - " + .endTime |
    [.facilityName, .rawActivity, .time, .weekday, .reservationRequired] |
    join(" | ")
' |
iconv -f utf-8 -t ascii//translit -c |
sort > $prefix.ours.txt

# TODO: do we need to filter ours for only currently active schedules?

git diff --no-index $prefix.ours.txt $prefix.theirs.txt > $prefix.$updated.diff
