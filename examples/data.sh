#!/usr/bin/env bash
# data.sh - simple script to interact with EdgeX's core-data
#   to view RO reports, tag data, and/or EPCs.
#   Use --help to view usage.

set -euo pipefail
IFS=$'\n\t'

HOST=localhost
DATA_PORT=59880
CURL_OPTS="-o-"
REMOVE_NULLS=0

usage() {
    echo "usage:"
    echo "  $0 --help"
    echo "     show this message and exit."
    echo "  $0 [OPTS] TARGET"
    echo "     show JSON-formatted data matching TARGET"
    echo ""
    echo "TARGET:"
    echo "    notifications       reader event notification"
    echo "    reports             full RO access reports"
    echo "    tags                just the tag data portion of reports"
    echo "    epcs                just the EPCs from tag data portions of reports"
    echo ""
    echo "OPTS:"
    echo "    -h | --host HOST    edgex-core-data host   default: localhost"
    echo "    -p | --port PORT    edgex-core-data port   default: 59880"
    echo "    -v | --verbose      use verbose curl output"
    echo "    -s | --silent       use silent curl output"
    echo "    -f | --filter-null  skip null values in reports and tags"
}

if [[ $# -lt 1 ]]; then
  usage; exit
fi

# read options
while [[ "$1" =~ ^- && ! "$1" == "--" ]]; do case $1 in
  -h | --host )
    shift; HOST=$1
    ;;
  -p | --core-cmds-port )
    shift; DATA_PORT=$1
    ;;
  -v | --verbose )
    CURL_OPTS="-vvvvo-"
    ;;
  -s | --silent )
    CURL_OPTS='-so-'
    ;;
  -f | --filter-null)
    REMOVE_NULLS=1
    ;;
  --help )
    usage; exit
    ;;
  *)
    echo "unknown flag $1"
    usage; exit
    ;;
esac; shift; done
if [[ "$1" == '--' ]]; then shift; fi

if [[ $# -lt 1 ]]; then
    echo "missing target"
    usage; exit
elif [[ $# -gt 1 ]]; then
    echo "extra args"
    usage; exit
fi

# get up to 1000 readings from EdgeX's core-data and return reading[].value as unquoted json
get() {
  TARGET=$1
  curl ${CURL_OPTS} "${HOST}":"${DATA_PORT}"/api/v2/reading/all?limit=50 | jq '.readings[].objectValue'
}

# based on the target, get some data and filter it
case "$1" in
    notifications)
        shift; get ReaderEventNotification
    ;;
    reports)
        if [[ $REMOVE_NULLS -eq 1 ]]; then
          # Strip nulls, remove empty reports, unwrap, and do a final filtering pass.
          shift; get ROAccessReport |
              jq 'with_entries(select(.value!=null))|select(any(.))|.[]|.[]|with_entries(select(.value!=null))'
        else
          shift; get ROAccessReport
        fi
    ;;
    tags)
        # LLRP distinguishes arbitrary data in the EPC memory bank vs bona fide EPC-96s,
        # but this filter destructure them into a common EPC field and removes the old ones.
        # A real client should probably keep them separate.
        if [[ $REMOVE_NULLS -eq 1 ]]; then
          shift; get ROAccessReport | \
            jq '.TagReportData[]?|with_entries(select(.value!=null))|.+{EPC:(.EPCData.EPC//.EPC96.EPC)}|del(.EPCData)|del(.EPC96)'
        else
          shift; get ROAccessReport | \
            jq '.TagReportData[]?|.+{EPC:(.EPCData.EPC//.EPC96.EPC)}|del(.EPCData)|del(.EPC96)'
        fi
    ;;
    epcs)
        # extract tags, decode from base64 to hex, then output how many times we saw each one.
        shift; get ROAccessReport | jq '.TagReportData[]?|.EPC96.EPC//.EPCData.EPC' | \
          tr -d '"' | base64 -d | od --endian=big -t x2 -An -w12 -v | sort | uniq -c
    ;;
    *)
        echo "unknown target: $1"
        usage
    ;;
esac


