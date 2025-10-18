#!/bin/bash
#
# This script compares data we've scraped to the data on simulacrumus's LLM-based scraper.
#
set -euxo pipefail
cd "$(dirname "$0")"

updated=$(set -e; curl -sL https://api.github.com/repos/simulacrumus/ottawa-drop-in-activity-scraper/commits/main | grep -Pom1 '(?<=Update schedules and caches \()[0-9-]+')

curl --fail -sL "https://github.com/simulacrumus/ottawa-drop-in-activity-scraper/raw/main/schedules.json" |
jq -cr '
    .[] |
    .dayOfWeek = ["", "monday", "tuesday","wednesday","thursday","friday","saturday","sunday"][.dayOfWeek] |
    [.facility, .activity, .startTime, .endTime, .dayOfWeek, .periodStartDate, .periodEndDate] |
    join(" | ")
' |
iconv -f utf-8 -t ascii//translit -c |
sort > theirs.txt

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
sort > ours.txt

git diff --no-index ours.txt theirs.txt > diff.txt
